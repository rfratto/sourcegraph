package db

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/felixfbecker/stringscore"
	"github.com/keegancsmith/sqlf"
	"github.com/pkg/errors"
	"github.com/sourcegraph/sourcegraph/pkg/api"
	"github.com/sourcegraph/sourcegraph/pkg/types"
	"github.com/sourcegraph/sourcegraph/pkg/vcs/git"
)

// TODO(slimsag:discussions): future: tests for DiscussionThreadsListOptions.TargetRepoID
// TODO(slimsag:discussions): future: tests for DiscussionThreadsListOptions.TargetRepoPath

// discussionThreads provides access to the `discussion_threads*` tables.
//
// For a detailed overview of the schema, see schema.md.
type discussionThreads struct{}

// ErrThreadNotFound is the error returned by Discussions methods to indicate
// that the thread could not be found.
type ErrThreadNotFound struct {
	// ThreadID is the thread that was not found.
	ThreadID int64
}

func (e *ErrThreadNotFound) Error() string {
	return fmt.Sprintf("thread %d not found", e.ThreadID)
}

func (t *discussionThreads) Create(ctx context.Context, newThread *types.DiscussionThread) (*types.DiscussionThread, error) {
	if Mocks.DiscussionThreads.Create != nil {
		return Mocks.DiscussionThreads.Create(ctx, newThread)
	}

	// Validate the input thread.
	if newThread == nil {
		return nil, errors.New("newThread is nil")
	}
	if newThread.ID != 0 {
		return nil, errors.New("newThread.ID must be zero")
	}
	if strings.TrimSpace(newThread.Title) == "" {
		return nil, errors.New("newThread.Title must be present (and not whitespace)")
	}
	if len([]rune(newThread.Title)) > 500 {
		return nil, errors.New("newThread.Title too long (must be less than 500 UTF-8 characters)")
	}
	if !newThread.CreatedAt.IsZero() {
		return nil, errors.New("newThread.CreatedAt must not be specified")
	}
	if newThread.ArchivedAt != nil {
		return nil, errors.New("newThread.ArchivedAt must not be specified")
	}
	if !newThread.UpdatedAt.IsZero() {
		return nil, errors.New("newThread.UpdatedAt must not be specified")
	}
	if newThread.DeletedAt != nil {
		return nil, errors.New("newThread.DeletedAt must not be specified")
	}
	if newThread.TargetRepo != nil {
		if rev := newThread.TargetRepo.Revision; rev != nil {
			if !git.IsAbsoluteRevision(*rev) {
				return nil, errors.New("newThread.TargetRepo.Revision must be an absolute Git revision (40 character SHA-1 hash)")
			}
		}
	} else {
		return nil, errors.New("newThread must have a target")
	}

	// TODO(slimsag:discussions): should be in a transaction

	// First, create the thread itself. Initially it will have no target.
	newThread.CreatedAt = time.Now()
	newThread.UpdatedAt = newThread.CreatedAt
	err := globalDB.QueryRowContext(ctx, `INSERT INTO discussion_threads(
		author_user_id,
		title,
		created_at,
		updated_at
	) VALUES ($1, $2, $3, $4) RETURNING id`,
		newThread.AuthorUserID,
		newThread.Title,
		newThread.CreatedAt,
		newThread.UpdatedAt,
	).Scan(&newThread.ID)
	if err != nil {
		return nil, errors.Wrap(err, "create thread")
	}

	// Create the thread target and have it reference the thread we just created.
	var (
		targetName string
		targetID   int64
	)
	switch {
	case newThread.TargetRepo != nil:
		var err error
		newThread.TargetRepo, err = t.createTargetRepo(ctx, newThread.TargetRepo, newThread.ID)
		if err != nil {
			return nil, errors.Wrap(err, "createTargetRepo")
		}
		targetName = "target_repo_id"
		targetID = newThread.TargetRepo.ID
	default:
		return nil, errors.New("unexpected target type")
	}

	// Update the thread to reference the target we just created.
	_, err = globalDB.ExecContext(ctx, `UPDATE discussion_threads SET `+targetName+`=$1 WHERE id=$2`, targetID, newThread.ID)
	if err != nil {
		return nil, errors.Wrap(err, "update thread target")
	}
	return newThread, nil
}

func (t *discussionThreads) Get(ctx context.Context, threadID int64) (*types.DiscussionThread, error) {
	if Mocks.DiscussionThreads.Get != nil {
		return Mocks.DiscussionThreads.Get(ctx, threadID)
	}
	threads, err := t.getBySQL(ctx, "WHERE (id=$1 AND deleted_at IS NULL) LIMIT 1", threadID)
	if err != nil {
		return nil, err
	}
	if len(threads) == 0 {
		return nil, &ErrThreadNotFound{ThreadID: threadID}
	}
	return threads[0], nil
}

type DiscussionThreadsUpdateOptions struct {
	// Archive, when non-nil, specifies whether the thread is archived or not.
	Archive *bool
}

func (t *discussionThreads) Update(ctx context.Context, threadID int64, opts *DiscussionThreadsUpdateOptions) (*types.DiscussionThread, error) {
	if Mocks.DiscussionThreads.Update != nil {
		return Mocks.DiscussionThreads.Update(ctx, threadID, opts)
	}
	if opts == nil {
		return nil, errors.New("options must not be nil")
	}
	now := time.Now()

	// TODO(slimsag:discussions): should be in a transaction

	anyUpdate := false
	if opts.Archive != nil {
		anyUpdate = true
		var archivedAt *time.Time
		if *opts.Archive {
			archivedAt = &now
		}
		if _, err := globalDB.ExecContext(ctx, "UPDATE discussion_threads SET archived_at=$1 WHERE id=$2", archivedAt, threadID); err != nil {
			return nil, err
		}
	}
	if anyUpdate {
		if _, err := globalDB.ExecContext(ctx, "UPDATE discussion_threads SET updated_at=$1 WHERE id=$2", now, threadID); err != nil {
			return nil, err
		}
	}
	return t.Get(ctx, threadID)
}

type DiscussionThreadsListOptions struct {
	// LimitOffset specifies SQL LIMIT and OFFSET counts. It may be nil (no limit / offset).
	*LimitOffset

	// TitleQuery, when non-nil, specifies that only threads whose title
	// matches this string should be returned.
	TitleQuery *string

	// ThreadID, when non-nil, specifies that only the thread with this ID
	// should be returned. This is the same as DiscussionThreads.Get, except in
	// the same API format.
	ThreadID *int64

	// AuthorUserID, when non-nil, specifies that only threads made by this
	// author should be returned.
	AuthorUserID *int32

	// TargetRepoID, when non-nil, specifies that only threads that have a repo target and
	// this repo ID should be returned.
	TargetRepoID *api.RepoID

	// TargetRepoPath, when non-nil, specifies that only threads that have a repo target
	// and this path should be returned.
	TargetRepoPath *string
}

func (t *discussionThreads) List(ctx context.Context, opts *DiscussionThreadsListOptions) ([]*types.DiscussionThread, error) {
	if Mocks.DiscussionThreads.List != nil {
		return Mocks.DiscussionThreads.List(ctx, opts)
	}
	if opts == nil {
		return nil, errors.New("options must not be nil")
	}
	conds := t.getListSQL(opts)
	q := sqlf.Sprintf("WHERE %s ORDER BY id DESC %s", sqlf.Join(conds, "AND"), opts.LimitOffset.SQL())

	threads, err := t.getBySQL(ctx, q.Query(sqlf.PostgresBindVar), q.Args()...)
	if err != nil {
		return nil, err
	}
	return t.fuzzyFilterThreads(opts, threads), nil
}

func (t *discussionThreads) Count(ctx context.Context, opts *DiscussionThreadsListOptions) (int, error) {
	if Mocks.DiscussionThreads.Count != nil {
		return Mocks.DiscussionThreads.Count(ctx, opts)
	}
	if opts == nil {
		return 0, errors.New("options must not be nil")
	}
	if opts.TitleQuery != nil {
		// TitleQuery requires post-query filtering (we must grab at least the
		// title of the thread). So we take the easy way out here and just
		// actually determine the results to find the count.
		threads, err := t.List(ctx, opts)
		return len(threads), err
	}
	conds := t.getListSQL(opts)
	q := sqlf.Sprintf("WHERE %s", sqlf.Join(conds, "AND"))
	return t.getCountBySQL(ctx, q.Query(sqlf.PostgresBindVar), q.Args()...)
}

func (t *discussionThreads) fuzzyFilterThreads(opts *DiscussionThreadsListOptions, threads []*types.DiscussionThread) []*types.DiscussionThread {
	if opts.TitleQuery != nil && strings.TrimSpace(*opts.TitleQuery) != "" {
		var (
			scoresByThread  = make(map[*types.DiscussionThread]int, len(threads))
			threadsToRemove []*types.DiscussionThread
		)
		for _, t := range threads {
			score := stringscore.Score(t.Title, *opts.TitleQuery)
			if score > 0 {
				scoresByThread[t] = score
			} else {
				threadsToRemove = append(threadsToRemove, t)
			}
		}
		for _, rm := range threadsToRemove {
			for i, t := range threads {
				if t == rm {
					threads = append(threads[:i], threads[i+1:]...)
					break
				}
			}
		}

		// TODO(slimsag:discussions): future: whether or not to sort based on
		// best match here should be optional.
		sort.Slice(threads, func(i, j int) bool {
			return scoresByThread[threads[i]] > scoresByThread[threads[j]]
		})
	}
	return threads
}

func (t *discussionThreads) Delete(ctx context.Context, threadID int64) error {
	res, err := globalDB.ExecContext(ctx, "UPDATE discussion_threads SET deleted_at=now() WHERE id=$1 AND deleted_at IS NULL", threadID)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return &ErrThreadNotFound{ThreadID: threadID}
	}
	return nil
}

func (*discussionThreads) getListSQL(opts *DiscussionThreadsListOptions) (conds []*sqlf.Query) {
	conds = []*sqlf.Query{sqlf.Sprintf("TRUE")}
	conds = append(conds, sqlf.Sprintf("deleted_at IS NULL"))
	if opts.TitleQuery != nil && strings.TrimSpace(*opts.TitleQuery) != "" {
		conds = append(conds, sqlf.Sprintf("title LIKE %v", extraFuzzy(*opts.TitleQuery)))
	}
	if opts.ThreadID != nil {
		conds = append(conds, sqlf.Sprintf("id=%v", *opts.ThreadID))
	}
	if opts.AuthorUserID != nil {
		conds = append(conds, sqlf.Sprintf("author_user_id=%v", *opts.AuthorUserID))
	}

	if opts.TargetRepoID != nil || opts.TargetRepoPath != nil {
		targetRepoConds := []*sqlf.Query{}
		if opts.TargetRepoID != nil {
			targetRepoConds = append(targetRepoConds, sqlf.Sprintf("repo_id=%v", *opts.TargetRepoID))
		}
		if opts.TargetRepoPath != nil {
			if strings.HasSuffix(*opts.TargetRepoPath, "/**") {
				match := strings.TrimSuffix(*opts.TargetRepoPath, "/**") + "%"
				targetRepoConds = append(targetRepoConds, sqlf.Sprintf("path LIKE %v", match))
			} else {
				targetRepoConds = append(targetRepoConds, sqlf.Sprintf("path=%v", *opts.TargetRepoPath))
			}
		}
		conds = append(conds, sqlf.Sprintf("id IN (SELECT id FROM discussion_threads_target_repo WHERE %v)", sqlf.Join(targetRepoConds, "AND")))
	}
	return conds
}

func (*discussionThreads) getCountBySQL(ctx context.Context, query string, args ...interface{}) (int, error) {
	var count int
	rows := globalDB.QueryRowContext(ctx, "SELECT count(id) FROM discussion_threads t "+query, args...)
	err := rows.Scan(&count)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return count, err
}

// createTargetRepo handles the creation of a repo-based discussion thread target.
func (t *discussionThreads) createTargetRepo(ctx context.Context, tr *types.DiscussionThreadTargetRepo, threadID int64) (*types.DiscussionThreadTargetRepo, error) {
	var fields []*sqlf.Query
	var values []*sqlf.Query
	field := func(name string, arg interface{}) {
		fields = append(fields, sqlf.Sprintf("%s", sqlf.Sprintf(name)))
		values = append(values, sqlf.Sprintf("%v", arg))
	}
	field("thread_id", threadID)
	field("repo_id", tr.RepoID)
	if tr.Path != nil {
		field("path", *tr.Path)
	}
	if tr.Branch != nil {
		field("branch", *tr.Branch)
	}
	if tr.Revision != nil {
		field("revision", *tr.Revision)
	}
	if tr.HasSelection() {
		field("start_line", *tr.StartLine)
		field("end_line", *tr.EndLine)
		field("start_character", *tr.StartCharacter)
		field("end_character", *tr.EndCharacter)
		field("lines_before", strings.Join(*tr.LinesBefore, "\n"))
		field("lines", strings.Join(*tr.Lines, "\n"))
		field("lines_after", strings.Join(*tr.LinesAfter, "\n"))
	}
	q := sqlf.Sprintf("INSERT INTO discussion_threads_target_repo(%v) VALUES (%v) RETURNING id", sqlf.Join(fields, ",\n"), sqlf.Join(values, ","))

	// To debug query building, uncomment these lines:
	//fmt.Println(q.Query(sqlf.PostgresBindVar))
	//fmt.Println(q.Args())

	err := globalDB.QueryRowContext(ctx, q.Query(sqlf.PostgresBindVar), q.Args()...).Scan(&tr.ID)
	if err != nil {
		return nil, err
	}
	return tr, err
}

// getBySQL returns threads matching the SQL query, if any exist.
func (t *discussionThreads) getBySQL(ctx context.Context, query string, args ...interface{}) ([]*types.DiscussionThread, error) {
	rows, err := globalDB.QueryContext(ctx, `
		SELECT
			t.id,
			t.author_user_id,
			t.title,
			t.target_repo_id,
			t.created_at,
			t.archived_at,
			t.updated_at
		FROM discussion_threads t `+query, args...)
	if err != nil {
		return nil, err
	}

	threads := []*types.DiscussionThread{}
	defer rows.Close()
	for rows.Next() {
		var (
			thread       types.DiscussionThread
			targetRepoID *int64
		)
		err := rows.Scan(
			&thread.ID,
			&thread.AuthorUserID,
			&thread.Title,
			&targetRepoID,
			&thread.CreatedAt,
			&thread.ArchivedAt,
			&thread.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		if targetRepoID != nil {
			thread.TargetRepo, err = t.getTargetRepo(ctx, *targetRepoID)
			if err != nil {
				return nil, errors.Wrap(err, "getTargetRepo")
			}
		}
		threads = append(threads, &thread)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return threads, nil
}

func (t *discussionThreads) getTargetRepo(ctx context.Context, targetRepoID int64) (*types.DiscussionThreadTargetRepo, error) {
	tr := &types.DiscussionThreadTargetRepo{}
	var linesBefore, lines, linesAfter *string
	err := globalDB.QueryRowContext(ctx, `
		SELECT
			t.id,
			t.thread_id,
			t.repo_id,
			t.path,
			t.branch,
			t.revision,
			t.start_line,
			t.end_line,
			t.start_character,
			t.end_character,
			t.lines_before,
			t.lines,
			t.lines_after
		FROM discussion_threads_target_repo t WHERE id=$1
	`, targetRepoID).Scan(
		&tr.ID,
		&tr.ThreadID,
		&tr.RepoID,
		&tr.Path,
		&tr.Branch,
		&tr.Revision,
		&tr.StartLine,
		&tr.EndLine,
		&tr.StartCharacter,
		&tr.EndCharacter,
		&linesBefore,
		&lines,
		&linesAfter,
	)
	if err != nil {
		return nil, err
	}
	if linesBefore != nil {
		linesBeforeSplit := strings.Split(*linesBefore, "\n")
		tr.LinesBefore = &linesBeforeSplit
	}
	if lines != nil {
		linesSplit := strings.Split(*lines, "\n")
		tr.Lines = &linesSplit
	}
	if linesAfter != nil {
		linesAfterSplit := strings.Split(*linesAfter, "\n")
		tr.LinesAfter = &linesAfterSplit
	}
	return tr, nil
}

// extraFuzzy turns a string like "cat" into "%c%a%t%". It can be used with a
// LIKE query to filter out results that cannot possibly match a fuzzy search
// query. This returns 'extra fuzzy' results, which are usually subsequently
// filtered in Go using github.com/felixfbecker/stringscore.
func extraFuzzy(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	input := []rune(s)

	result := make([]rune, 0, 1+(len(input)*2))
	result = append(result, '%')
	for _, r := range input {
		result = append(result, r)
		result = append(result, '%')
	}
	return string(result)
}