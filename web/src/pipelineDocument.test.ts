import { describe, expect, it } from 'vitest'
import { changeSequence, editDocument, indentSelection, readDocument, replaceEmptyStep, starter, newWatch } from './pipelineDocument'

const source = `# document comment
queue: { workers: 2 } # queue comment
values:
  root: /input
custom: untouched # keep unknown keys for Go diagnostics
watches:
  - name: images # watch comment
    path: /data
    pipeline:
      - name: one
        program: echo
        args: ['{{file}}', 'a b'] # argument comment
      - name: two
        program: cat
`
describe('pipeline YAML document', () => {
  it('parses YAML into the visual model, including templates', () => {
    const { model } = readDocument(source)
    expect(model.watches).toMatchObject([{ name: 'images', pipeline: [{ args: ['{{file}}', 'a b'] }, { name: 'two' }] }])
  })
  it('rejects syntax and unrepresentable structure without changing the source', () => {
    expect(() => readDocument('watches: [')).toThrow()
    expect(() => readDocument('watches: {}')).toThrow('sequence')
    expect(() => readDocument('watches: [{pipeline: [null]}]')).toThrow('mapping')
    expect(() => readDocument('watches: []\n---\n{}')).toThrow()
  })
  it('patches scalar edits without changing any other bytes', () => {
    expect(editDocument(source, ['watches', 0, 'name'], 'new name')).toBe(source.replace('name: images', 'name: new name'))
    expect(readDocument(editDocument(source, ['watches', 0, 'pipeline', 0, 'args', 0], '{{basename}}')).model.watches).toMatchObject([{ pipeline: [{ args: ['{{basename}}', 'a b'] }, {}] }])
  })
  it('keeps flow scalars and anchored scalars valid when edited', () => {
    const flow = editDocument('watches: [{name: old, path: /data, pipeline: []}]\n', ['watches', 0, 'name'], 'a, b')
    expect(readDocument(flow).model.watches).toMatchObject([{ name: 'a, b' }])
    const anchored = editDocument('values: {base: &root /data, copy: *root}\nwatches: []\n', ['values', 'base'], '/new')
    expect(readDocument(anchored).model.values).toEqual({ base: '/new', copy: '/new' })
  })
  it('adds and removes watches and steps, allowing incomplete drafts', () => {
    let text = changeSequence(starter, ['watches'], 'add', 0, newWatch(1))
    text = changeSequence(text, ['watches', 0, 'pipeline'], 'add')
    expect(readDocument(text).model.watches).toMatchObject([{ pipeline: [{}] }])
    text = changeSequence(text, ['watches', 0, 'pipeline'], 'remove', 0)
    expect(readDocument(text).model.watches).toMatchObject([{ pipeline: [] }])
    text = changeSequence(text, ['watches'], 'remove', 0)
    expect(readDocument(text).model.watches).toEqual([])
  })
  it('reorders nodes with comments and unrelated keys preserved', () => {
    const text = changeSequence(source, ['watches', 0, 'pipeline'], 'down', 0)
    expect(readDocument(text).model.watches).toMatchObject([{ pipeline: [{ name: 'two' }, { name: 'one' }] }])
    for (const comment of ['# document comment', '# queue comment', '# watch comment', '# argument comment']) expect(text).toContain(comment)
    expect(text).toContain('{{file}}')
    expect(readDocument(text).model.custom).toBe('untouched')
    expect(readDocument(changeSequence(text, ['watches', 0, 'pipeline'], 'up', 1)).model).toEqual(readDocument(source).model)
  })
  it('preserves collection comments when a visual argument is edited', () => {
    const text = editDocument(source, ['watches', 0, 'pipeline', 0, 'args'], ['{{file}}', 'new argument'])
    expect(text).toContain('# argument comment')
    expect(readDocument(text).model.watches).toMatchObject([{ pipeline: [{ args: ['{{file}}', 'new argument'] }, {}] }])
  })
  it('accepts parsed fragments only into an empty selected step', () => {
    const text = changeSequence(source, ['watches', 0, 'pipeline'], 'add')
    const fragment = 'pipeline:\n- name: docker-run\n  executor: docker\n  image: alpine\n'
    const updated = replaceEmptyStep(text, ['watches', 0, 'pipeline', 2], fragment)
    expect(readDocument(updated).model.watches).toMatchObject([{ pipeline: [{}, {}, { image: 'alpine' }] }])
    expect(() => replaceEmptyStep(updated, ['watches', 0, 'pipeline', 2], fragment)).toThrow('no longer empty')
    expect(() => replaceEmptyStep(updated, ['watches', 0, 'pipeline', 99], fragment)).toThrow()
  })
})

it('indents and unindents selected YAML lines without replacing them', () => {
  const text = 'one:\n  two: three\n'
  const indented = indentSelection(text, 0, text.length, false)
  expect(indented.text).toBe('  one:\n    two: three\n')
  expect(indentSelection(indented.text, 0, indented.text.length, true).text).toBe(text)
})
