import { isMap, isScalar, isSeq, parseDocument, stringify, type Document, type Scalar } from 'yaml'

export type Path = (string | number)[]
export type RecordValue = Record<string, unknown>
export const starter = `# Edit this draft, then validate and export it.\nqueue:\n  workers: 2\nwatches: []\n`
export const newWatch = (number: number) => ({ name: `watch-${number}`, path: './input', recursive: true, pipeline: [] })
export const record = (value: unknown): RecordValue => value !== null && typeof value === 'object' && !Array.isArray(value) ? value as RecordValue : {}
export const list = (value: unknown): unknown[] => Array.isArray(value) ? value : []
export const emptyStep = (value: unknown) => Object.keys(record(value)).every(key => key === 'name')

export function readDocument(text: string) {
  const doc = parseDocument(text, { keepSourceTokens: true })
  if (doc.errors.length) throw new Error(doc.errors.map(e => e.message).join('\n'))
  if (!isMap(doc.contents)) throw new Error('The configuration must be a YAML mapping.')
  const model = record(doc.toJS({ maxAliasCount: 100 }))
  // Incomplete fields are editable, but malformed structural containers cannot
  // safely be represented as cards. Keep the last usable preview in that case.
  if (model.watches !== undefined && !Array.isArray(model.watches)) throw new Error('watches must be a sequence.')
  for (const watch of list(model.watches)) {
    if (watch === null || typeof watch !== 'object' || Array.isArray(watch)) throw new Error('Each watch must be a mapping.')
    const pipeline = record(watch).pipeline
    if (pipeline !== undefined && !Array.isArray(pipeline)) throw new Error('pipeline must be a sequence.')
    if (list(pipeline).some(step => step === null || typeof step !== 'object' || Array.isArray(step))) throw new Error('Each step must be a mapping; use {} for an empty step.')
  }
  return { doc, model }
}

export function editDocument(text: string, path: Path, value: unknown): string {
  const { doc } = readDocument(text)
  const current = doc.getIn(path, true)
  // Patch scalar source ranges directly: unrelated whitespace, ordering,
  // comments and custom YAML remain byte-for-byte unchanged on ordinary edits.
  const inFlow = path.some((_, index) => { const parent = doc.getIn(path.slice(0, index), true); return (isMap(parent) || isSeq(parent)) && parent.flow })
  if (isScalar(current) && current.range && !current.anchor && !current.tag && !inFlow && value !== undefined && (value === null || typeof value !== 'object')) {
    const replacement = current.clone() as Scalar
    replacement.value = value
    replacement.comment = undefined
    replacement.commentBefore = undefined
    const rendered = stringify(replacement, { lineWidth: 0 }).trimEnd()
    if (!rendered.includes('\n') && !text.slice(current.range[0], current.range[1]).includes('\n')) {
      return text.slice(0, current.range[0]) + rendered + text.slice(current.range[1])
    }
  }
  if (value === undefined) doc.deleteIn(path)
  else reconcile(doc, path, value)
  return doc.toString({ lineWidth: 0 })
}

export function changeSequence(text: string, path: Path, action: 'add' | 'remove' | 'up' | 'down', index = 0, value: unknown = {}) {
  const { doc } = readDocument(text)
  if (doc.getIn(path) === undefined) doc.setIn(path, [])
  const sequence = doc.getIn(path, true)
  if (!isSeq(sequence)) throw new Error('Expected a YAML sequence.')
  if (action === 'add') {
    if (!sequence.items.length) sequence.flow = false
    sequence.add(doc.createNode(value))
  }
  else if (action === 'remove') sequence.items.splice(index, 1)
  else {
    const target = index + (action === 'up' ? -1 : 1)
    if (index < 0 || target < 0 || index >= sequence.items.length || target >= sequence.items.length) return text
    ;[sequence.items[index], sequence.items[target]] = [sequence.items[target], sequence.items[index]]
  }
  return doc.toString({ lineWidth: 0 })
}

export function replaceEmptyStep(text: string, path: Path, fragment: string) {
  const { doc } = readDocument(text)
  const current = doc.getIn(path, true)
  if (!isMap(current) || !emptyStep(current.toJSON())) throw new Error('The selected step is no longer empty.')
  const generated = parseDocument(fragment)
  if (generated.errors.length) throw new Error('Invalid parser response.')
  const step = generated.getIn(['pipeline', 0], true)
  if (!isMap(step)) throw new Error('Missing generated step.')
  doc.setIn(path, step.clone())
  return doc.toString({ lineWidth: 0 })
}

export function lineForPath(doc: Document, text: string, path: Path) {
  const node = doc.getIn(path, true)
  return (isMap(node) || isSeq(node) || isScalar(node)) && node.range ? text.slice(0, node.range[0]).split('\n').length : 1
}

// Keep existing nodes (and their comments/styles) when editing collection fields.
function reconcile(doc: Document, path: Path, value: unknown) {
  const current = doc.getIn(path, true)
  if (isMap(current) && value !== null && typeof value === 'object' && !Array.isArray(value)) {
    const next = record(value)
    for (const pair of [...current.items]) {
      const key = isScalar(pair.key) ? String(pair.key.value) : ''
      if (key && !Object.hasOwn(next, key)) current.delete(key)
    }
    for (const [key, item] of Object.entries(next)) reconcile(doc, [...path, key], item)
  } else if (isSeq(current) && Array.isArray(value)) {
    current.items.splice(value.length)
    value.forEach((item, index) => reconcile(doc, [...path, index], item))
  } else doc.setIn(path, value)
}

export function indentSelection(text: string, start: number, end: number, unindent: boolean) {
  if (!unindent && !text.slice(start, end).includes('\n')) return { text: text.slice(0, start) + '  ' + text.slice(end), start: start + 2, end: start + 2 }
  const first = text.lastIndexOf('\n', start - 1) + 1
  const last = end > start && text[end - 1] === '\n' ? end - 1 : end
  const lines = text.slice(first, last).split('\n')
  const changes = lines.map(line => unindent ? -Math.min(2, line.match(/^ */)![0].length) : 2)
  const replacement = lines.map((line, i) => changes[i] < 0 ? line.slice(-changes[i]) : ' '.repeat(changes[i]) + line).join('\n')
  return { text: text.slice(0, first) + replacement + text.slice(last), start: Math.max(first, start + changes[0]), end: Math.max(first, end + changes.reduce((a, b) => a + b, 0)) }
}
