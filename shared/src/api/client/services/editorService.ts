import { Selection } from '@sourcegraph/extension-api-types'
import { BehaviorSubject, combineLatest, Subscribable } from 'rxjs'
import { map } from 'rxjs/operators'
import { TextDocumentPositionParams } from '../../protocol'
import { ModelService, TextModel } from './modelService'
/**
 * EditorId exposes the unique ID of an editor.
 */
export interface EditorId {
    /** The unique ID of the editor. */
    readonly editorId: string
}

/**
 * Describes a code editor to be created.
 */
export interface CodeEditorData {
    readonly type: 'CodeEditor'

    /** The URI of the model that this editor is displaying. */
    readonly resource: string

    readonly selections: Selection[]
    readonly collapsed?: boolean
    readonly isActive: boolean
}

/**
 * Describes a code editor that has been added to the {@link EditorService}.
 */
export interface CodeEditor extends EditorId, CodeEditorData {
    /**
     * The model that represents the editor's document (and includes its contents).
     */
    readonly model: TextModel
}

/**
 * The editor service manages editors and documents.
 */
export interface EditorService {
    /** All code editors, with each editor's model. */
    readonly editors: Subscribable<readonly CodeEditor[]>

    /**
     * Add an editor.
     *
     * @param editor The description of the editor to add.
     * @returns The added code editor (which must be passed as the first argument to other
     * {@link EditorService} methods to operate on this editor).
     */
    addEditor(editor: CodeEditorData): EditorId

    /**
     * Sets the selections for an editor.
     *
     * @param editor The editor for which to set the selections.
     * @param selections The new selections to apply.
     */
    setSelections(editor: EditorId, selections: Selection[]): void

    /**
     * Remove an editor.
     *
     * @param editor The editor to remove.
     */
    removeEditor(editor: EditorId): void

    /**
     * Remove all editors.
     */
    removeAllEditors(): void

    /**
     * Collapse or expand an editor.
     *
     * @param editor The editor to collapse or expand.
     * @param collapsed The desired state.
     */
    setCollapsed(editor: EditorId, collapsed: boolean): void
}

/**
 * Creates a {@link EditorService} instance.
 */
export function createEditorService(modelService: Pick<ModelService, 'models'>): EditorService {
    let id = 0
    const nextId = () => `editor#${id++}`

    const findModelForEditor = (models: readonly TextModel[], { resource }: Pick<CodeEditorData, 'resource'>) => {
        const model = models.find(m => m.uri === resource)
        if (!model) {
            throw new Error(`editor model not found: ${resource}`)
        }
        return model
    }

    type AddedCodeEditor = Pick<CodeEditor, Exclude<keyof CodeEditor, 'model'>>
    const editors = new BehaviorSubject<readonly AddedCodeEditor[]>([])
    return {
        editors: combineLatest(editors, modelService.models).pipe(
            map(([editors, models]) =>
                editors.map(editor => ({ ...editor, model: findModelForEditor(models, editor) }))
            )
        ),
        addEditor: data => {
            const editor: AddedCodeEditor = { ...data, editorId: nextId() }
            editors.next([...editors.value, editor])
            return editor
        },
        setSelections({ editorId }: EditorId, selections: Selection[]): void {
            editors.next([
                ...editors.value.filter(e => e.editorId !== editorId),
                ...editors.value.filter(e => e.editorId === editorId).map(e => ({ ...e, selections })),
            ])
        },
        removeEditor({ editorId }: EditorId): void {
            editors.next(editors.value.filter(e => e.editorId !== editorId))
        },
        removeAllEditors(): void {
            editors.next([])
        },
        setCollapsed({ editorId }: EditorId, collapsed: boolean): void {
            editors.next([
                ...editors.value.filter(e => e.resource !== editorId),
                ...editors.value.filter(e => e.resource === editorId).map(e => ({ ...e, collapsed })),
            ])
        },
    }
}

/**
 * Helper function to get the active editor's {@link TextDocumentPositionParams} from
 * {@link EditorService#editors}. If there is no active editor or it has no position, it returns
 * null.
 */
export function getActiveCodeEditorPosition(editors: readonly CodeEditorData[]): TextDocumentPositionParams | null {
    const activeEditor = editors.find(({ isActive }) => isActive)
    if (!activeEditor) {
        return null
    }
    const sel = activeEditor.selections[0]
    if (!sel) {
        return null
    }
    // TODO(sqs): Return null for empty selections (but currently all selected tokens are treated as an empty
    // selection at the beginning of the token, so this would break a lot of things, so we only do this for empty
    // selections when the start character is -1). HACK(sqs): Character === -1 means that the whole line is
    // selected (this is a bug in the caller, but it is useful here).
    const isEmpty =
        sel.start.line === sel.end.line && sel.start.character === sel.end.character && sel.start.character === -1
    if (isEmpty) {
        return null
    }
    return {
        textDocument: { uri: activeEditor.resource },
        position: sel.start,
    }
}
