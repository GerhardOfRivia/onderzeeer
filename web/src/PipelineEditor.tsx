import { useEffect, useMemo, useRef, useState } from 'react'
import { Accordion, AccordionDetails, AccordionSummary, Alert, Box, Button, Card, CardContent, Chip, Dialog, DialogActions, DialogContent, DialogTitle, FormControlLabel, MenuItem, Stack, Switch, TextField, ThemeProvider, Typography, createTheme, useMediaQuery } from '@mui/material'
import { request } from './api'
import { changeSequence, editDocument, emptyStep, indentSelection, lineForPath, list, newWatch, readDocument, record, replaceEmptyStep, starter, type Path, type RecordValue } from './pipelineDocument'
import type { ThemePreference } from './theme'
import './pipelineEditor.css'

type Diagnostic = { kind: string; severity: string; message: string; path?: string; line?: number; column?: number }
type Validation = { valid: boolean; diagnostics: Diagnostic[] }
type Selection = { watch: number; step: number } | null
const maxFileBytes = 1024 * 1024

export default function PipelineEditor({ token, theme, onDirtyChange }: { token: string; theme: ThemePreference; onDirtyChange: (dirty: boolean) => void }) {
  const [text, setText] = useState(starter)
  const [saved, setSaved] = useState(starter)
  const [filename, setFilename] = useState('pipeline.yaml')
  const [selection, setSelection] = useState<Selection>(null)
  const [validation, setValidation] = useState<Validation | null>(null)
  const [validationError, setValidationError] = useState('')
  const [validating, setValidating] = useState(false)
  const [attempt, setAttempt] = useState(0)
  const [message, setMessage] = useState('')
  const [ratio, setRatio] = useState(50)
  const [parseOpen, setParseOpen] = useState(false)
  const [command, setCommand] = useState('')
  const [stepName, setStepName] = useState('')
  const [preview, setPreview] = useState<{ yaml: string; warnings: string[] } | null>(null)
  const [parseError, setParseError] = useState('')
  const [parsing, setParsing] = useState(false)
  const [confirm, setConfirm] = useState<{ title: string; action: () => void } | null>(null)
  const input = useRef<HTMLTextAreaElement>(null)
  const upload = useRef<HTMLInputElement>(null)
  const split = useRef<HTMLDivElement>(null)
  const gutter = useRef<HTMLPreElement>(null)
  const lastGood = useRef(readDocument(starter))
  const latestText = useRef(text)
  latestText.current = text
  const history = useRef({ past: [] as string[], future: [] as string[] })
  const parseGeneration = useRef(0)
  const immediateValidation = useRef(false)
  const dirty = text !== saved
  const systemDark = useMediaQuery('(prefers-color-scheme: dark)')
  const muiTheme = useMemo(() => createTheme({ palette: { mode: theme === 'dark' || theme === 'system' && systemDark ? 'dark' : 'light', primary: { main: '#43896d' } }, typography: { fontFamily: 'inherit', fontSize: 13 }, components: { MuiButton: { styleOverrides: { root: { textTransform: 'none' } } } } }), [theme, systemDark])
  const parsed = useMemo(() => {
    try { return { value: readDocument(text), error: '' } }
    catch (error) { return { value: null, error: error instanceof Error ? error.message : 'Invalid YAML' } }
  }, [text])
  if (parsed.value) lastGood.current = parsed.value
  const model = (parsed.value ?? lastGood.current).model
  const watches = list(model.watches)
  const selected = selection ? list(record(watches[selection.watch]).pipeline)[selection.step] : undefined
  const canParse = Boolean(parsed.value && selection && selected !== undefined && emptyStep(selected) && token)

  useEffect(() => { onDirtyChange(dirty) }, [dirty, onDirtyChange])
  useEffect(() => {
    if (!dirty) return
    const warn = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = '' }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [dirty])
  useEffect(() => {
    if (selection && (selection.watch >= watches.length || selection.step >= list(record(watches[selection.watch]).pipeline).length)) setSelection(null)
  }, [text]) // Preserve selection by position while typing and explicitly move it on reorder.
  useEffect(() => {
    const controller = new AbortController()
    setValidation(null); setValidationError(''); setValidating(Boolean(token))
    if (!token) { setValidationError('Unlock controls to validate with the Go configuration rules.'); return }
    const delay = immediateValidation.current ? 0 : 450
    immediateValidation.current = false
    const timer = window.setTimeout(() => {
      void request<Validation>('/api/v1/pipelines/validate', token, { method: 'POST', body: JSON.stringify({ yaml: text }), signal: controller.signal })
        .then(result => { if (!controller.signal.aborted) setValidation(result) })
        .catch(error => { if (!controller.signal.aborted) setValidationError(error.message) })
        .finally(() => { if (!controller.signal.aborted) setValidating(false) })
    }, delay)
    return () => { clearTimeout(timer); controller.abort() }
  }, [text, token, attempt])

  function update(value: string) {
    if (value === latestText.current) return
    history.current.past.push(latestText.current)
    if (history.current.past.length > 200) history.current.past.shift()
    history.current.future = []
    setText(value); setValidation(null)
  }
  function undo(redo = false) {
    const from = redo ? history.current.future : history.current.past
    const to = redo ? history.current.past : history.current.future
    const value = from.pop()
    if (value !== undefined) { to.push(text); setText(value) }
  }
  function mutate(action: () => string) {
    try { update(action()); setMessage('') } catch (error) { setMessage((error as Error).message) }
  }
  function field(path: Path, value: unknown) { mutate(() => editDocument(latestText.current, path, value)) }
  function discard(title: string, action: () => void) { if (dirty) setConfirm({ title, action }); else action() }
  function load(value: string, name: string) {
    update(value); setSaved(value); setFilename(name); setSelection(null); setMessage('')
  }
  async function importFile(file: File) {
    if (file.size > maxFileBytes) { setMessage('Choose a YAML file smaller than 1 MiB.'); return }
    try {
      const value = await file.text()
      // Check current dirty state after the asynchronous read, not at selection.
      setConfirm({ title: 'Load this file? Any current edits will be replaced.', action: () => load(value, file.name) })
    } catch (error) { setMessage(`Could not read file: ${(error as Error).message}`) }
  }
  function exportFile() {
    const url = URL.createObjectURL(new Blob([text], { type: 'application/yaml;charset=utf-8' }))
    const link = document.createElement('a'); link.href = url; link.download = filename; link.click()
    setTimeout(() => URL.revokeObjectURL(url), 1000); setSaved(text)
  }
  function goToLine(line = 1) {
    const position = text.split('\n').slice(0, Math.max(0, line - 1)).reduce((total, value) => total + value.length + 1, 0)
    input.current?.focus(); input.current?.setSelectionRange(position, position)
    if (input.current) input.current.scrollTop = Math.max(0, (line - 3) * 21)
  }
  function openParser() {
    parseGeneration.current++; setPreview(null); setParseError(''); setCommand(''); setStepName(String(record(selected).name ?? '')); setParseOpen(true)
  }
  async function parseCommand() {
    const generation = ++parseGeneration.current
    setParsing(true); setParseError(''); setPreview(null)
    try {
      const result = await request<{ yaml: string; warnings: string[] }>('/api/v1/pipelines/parse', token, { method: 'POST', body: JSON.stringify({ command, name: stepName }) })
      if (generation === parseGeneration.current) setPreview(result)
    } catch (error) { if (generation === parseGeneration.current) setParseError((error as Error).message) }
    finally { if (generation === parseGeneration.current) setParsing(false) }
  }
  const status = validation?.valid ? validation.diagnostics.length ? 'warning' : 'success' : validation ? validation.diagnostics.some(item => item.severity === 'error') ? 'error' : 'warning' : 'warning'
  const set = (base: Path) => (key: string, value: unknown) => field([...base, key], value)

  return <ThemeProvider theme={muiTheme}><section className="pipeline-editor" aria-label="Pipeline Editor">
    <Stack direction="row" spacing={1} sx={{ mb: 1, alignItems: 'center', flexWrap: 'wrap' }}>
      <Typography variant="h5" component="h1">Pipeline Editor</Typography>
      <Chip size="small" label={dirty ? 'Unsaved changes' : filename} color={dirty ? 'warning' : 'default'} />
    </Stack>
    <Typography variant="body2" sx={{ mb: 1 }}>Edit a local YAML draft. Exporting never changes a running instance; re-register and restart explicitly to apply a new snapshot.</Typography>
    {message && <Alert severity="error" onClose={() => setMessage('')}>{message}</Alert>}
    <div className="pipeline-split" ref={split} style={{ gridTemplateColumns: `${ratio}% 8px minmax(0, 1fr)` }}>
      <section className="yaml-panel" aria-label="YAML editor">
        <Stack direction="row" sx={{ gap: 0.5, flexWrap: 'wrap' }} className="yaml-toolbar">
          <Button onClick={() => discard('Discard unsaved edits and create a new configuration?', () => load(starter, 'pipeline.yaml'))}>New</Button>
          <Button onClick={() => upload.current?.click()}>Import YAML</Button>
          <input ref={upload} type="file" accept=".yaml,.yml,text/yaml,application/yaml" hidden onChange={event => { const file = event.target.files?.[0]; if (file) void importFile(file); event.target.value = '' }} />
          <Button onClick={exportFile}>Export</Button>
          <Button onClick={() => undo()} disabled={!history.current.past.length}>Undo</Button>
          <Button onClick={() => undo(true)} disabled={!history.current.future.length}>Redo</Button>
          <Button disabled={!token} onClick={() => { immediateValidation.current = true; setAttempt(value => value + 1) }}>Validate</Button>
          <Button disabled={!canParse} onClick={openParser}>Parse Command</Button>
        </Stack>
        <div className="yaml-source">
          <pre className="yaml-lines" ref={gutter} aria-hidden="true">{text.split('\n').map((_, index) => index + 1).join('\n')}</pre>
          <textarea ref={input} aria-label="Pipeline YAML" spellCheck={false} value={text} wrap="off" onChange={event => update(event.target.value)} onScroll={event => { if (gutter.current) gutter.current.scrollTop = event.currentTarget.scrollTop }} onKeyDown={event => {
            if ((event.ctrlKey || event.metaKey) && ['z', 'y'].includes(event.key.toLowerCase())) { event.preventDefault(); undo(event.shiftKey || event.key.toLowerCase() === 'y'); return }
            if (event.key === 'Tab') {
              event.preventDefault(); const target = event.currentTarget; const start = target.selectionStart; const end = target.selectionEnd
              const next = indentSelection(text, start, end, event.shiftKey)
              update(next.text); requestAnimationFrame(() => target.setSelectionRange(next.start, next.end))
            }
          }} />
        </div>
        <div className="validation-status" aria-live="polite">
          <Alert severity={status}>{validating ? 'Validating…' : validationError || (validation?.valid ? validation.diagnostics.length ? 'Valid configuration with warnings' : 'Valid YAML and onderzeeer configuration' : validation ? status === 'warning' ? 'Incomplete configuration' : 'Configuration needs attention' : 'Incomplete draft — awaiting validation')}</Alert>
          {validation?.diagnostics.map((item, index) => <button className="validation-diagnostic" key={index} onClick={() => goToLine(item.line)}>{item.line ? `Line ${item.line}: ` : ''}{item.message}</button>)}
          {parsed.error && <Typography color="error" variant="body2">{parsed.error}</Typography>}
        </div>
      </section>
      <div className="pipeline-divider" role="separator" aria-label="Resize editor panels" aria-orientation="vertical" aria-valuemin={25} aria-valuemax={75} aria-valuenow={ratio} tabIndex={0} onKeyDown={event => { if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') { event.preventDefault(); setRatio(value => Math.max(25, Math.min(75, value + (event.key === 'ArrowLeft' ? -2 : 2)))) } }} onPointerDown={event => event.currentTarget.setPointerCapture(event.pointerId)} onPointerMove={event => { if (event.currentTarget.hasPointerCapture(event.pointerId) && split.current) { const bounds = split.current.getBoundingClientRect(); setRatio(Math.max(25, Math.min(75, 100 * (event.clientX - bounds.left) / bounds.width))) } }} onPointerUp={event => event.currentTarget.releasePointerCapture(event.pointerId)} />
      <section className="visual-panel" aria-label="Visual pipeline editor">
        {parsed.error && <Alert severity="warning">Stale preview: fix the YAML structure to resume visual editing.</Alert>}
        <fieldset disabled={!parsed.value} className="visual-fields">
          <Accordion><AccordionSummary>General configuration</AccordionSummary><AccordionDetails>
            <Typography variant="subtitle2">Queue</Typography>
            <Fields value={record(model.queue)} onChange={set(['queue'])} fields={{ workers: 'number', max_retries: 'number', retry_delay: 'text' }} />
            <Typography variant="subtitle2">Database</Typography>
            <Fields value={record(model.database)} onChange={set(['database'])} fields={{ path: 'text', output_retention: 'text', retention_include_failed: 'boolean', warn_size_bytes: 'number', warn_free_percent: 'number' }} />
            <Fields value={model} onChange={set([])} fields={{ values: 'map', resources: 'list' }} />
          </AccordionDetails></Accordion>
          {!watches.length && <Alert severity="info" sx={{ my: 2 }}>Add a watch, then add an empty step. Select the step to populate it with Parse Command, or enter its fields directly.</Alert>}
          {watches.map((watchValue, wi) => {
            const watch = record(watchValue); const steps = list(watch.pipeline); const watchPath: Path = ['watches', wi]
            return <Accordion key={wi} defaultExpanded><AccordionSummary>{String(watch.name ?? `Watch ${wi + 1}`)} — {String(watch.path ?? 'Choose a directory')}</AccordionSummary><AccordionDetails>
              <Fields value={watch} onChange={set(watchPath)} fields={{ name: 'text', path: 'text', include: 'list', exclude: 'list', recursive: 'boolean', settle_for: 'text', process_existing: 'boolean', reprocess_on_change: 'boolean' }} />
              <Button color="error" onClick={() => setConfirm({ title: `Remove watch ${String(watch.name ?? wi + 1)} and all its steps?`, action: () => { mutate(() => changeSequence(latestText.current, ['watches'], 'remove', wi)); setSelection(null) } })}>Remove Watch</Button>
              {steps.map((stepValue, si) => {
                const step = record(stepValue); const path = [...watchPath, 'pipeline', si]; const executor = String(step.executor ?? 'command'); const container = ['docker', 'podman', 'apptainer'].includes(executor)
                const incompatible = (executor === 'command' ? ['image', 'mounts', 'container_env', 'container_args', 'command', 'command_args'] : executor === 'shell' ? ['image', 'mounts', 'container_env', 'container_args', 'args'] : []).filter(key => Object.hasOwn(step, key))
                return <Card variant="outlined" key={`${wi}-${si}`} sx={{ mt: 1, borderColor: selection?.watch === wi && selection.step === si ? 'primary.main' : undefined }}>
                  <CardContent>
                    <Stack direction="row" sx={{ flexWrap: 'wrap', alignItems: 'center', gap: 1 }}>
                      <Typography variant="subtitle1">{si + 1}. {String(step.name ?? 'Empty step')}</Typography><Chip size="small" label={executor} />
                      <Button aria-label={`Edit step ${si + 1}`} onClick={() => { setSelection({ watch: wi, step: si }); goToLine(lineForPath(lastGood.current.doc, text, path)) }}>Edit Step</Button>
                      <Button disabled={si === 0} onClick={() => { mutate(() => changeSequence(text, [...watchPath, 'pipeline'], 'up', si)); setSelection({ watch: wi, step: si - 1 }) }}>Move Up</Button>
                      <Button disabled={si === steps.length - 1} onClick={() => { mutate(() => changeSequence(text, [...watchPath, 'pipeline'], 'down', si)); setSelection({ watch: wi, step: si + 1 }) }}>Move Down</Button>
                      <Button color="error" onClick={() => setConfirm({ title: 'Remove this step?', action: () => { mutate(() => changeSequence(latestText.current, [...watchPath, 'pipeline'], 'remove', si)); setSelection(null) } })}>Remove Step</Button>
                    </Stack>
                    <Fields value={step} onChange={set(path)} fields={{ name: 'text' }} />
                    <TextField select fullWidth size="small" label="Executor" value={executor} onChange={event => field([...path, 'executor'], event.target.value)} sx={{ my: 1 }}>
                      {!['command', 'shell', 'docker', 'podman', 'apptainer'].includes(executor) && <MenuItem value={executor}>{executor} (unknown)</MenuItem>}
                      {['command', 'shell', 'docker', 'podman', 'apptainer'].map(value => <MenuItem key={value} value={value}>{value}</MenuItem>)}
                    </TextField>
                    {incompatible.length > 0 && <Alert severity="warning">Fields incompatible with {executor}: {incompatible.join(', ')}. <Button onClick={() => mutate(() => incompatible.reduce((current, key) => editDocument(current, [...path, key], undefined), text))}>Clear incompatible fields</Button></Alert>}
                    <Fields value={step} onChange={set(path)} fields={{ program: 'text', ...(executor === 'command' ? { args: 'list' as const } : executor === 'shell' ? { command: 'multiline' as const, command_args: 'list' as const } : { image: 'text' as const, container_args: 'list' as const, mounts: 'mounts' as const, container_env: 'map' as const, command: 'text' as const, command_args: 'list' as const, args: 'list' as const }), timeout: 'text', working_directory: 'text', output: 'text', env: 'map', resources: 'text' }} />
                    {container && <Typography variant="caption">Use image and structured container fields, or raw args. Clear incompatible fields when changing executor.</Typography>}
                  </CardContent>
                </Card>
              })}
              <Button onClick={() => { mutate(() => changeSequence(text, [...watchPath, 'pipeline'], 'add')); setSelection({ watch: wi, step: steps.length }) }}>Add Step</Button>
            </AccordionDetails></Accordion>
          })}
          <Button variant="contained" sx={{ mt: 2 }} onClick={() => mutate(() => changeSequence(text, ['watches'], 'add', 0, newWatch(watches.length + 1)))}>Add Watch</Button>
        </fieldset>
      </section>
    </div>
    <Dialog open={Boolean(confirm)} onClose={() => setConfirm(null)}><DialogTitle>{confirm?.title}</DialogTitle><DialogActions><Button onClick={() => setConfirm(null)}>Cancel</Button><Button color="error" onClick={() => { confirm?.action(); setConfirm(null) }}>Confirm</Button></DialogActions></Dialog>
    <Dialog open={parseOpen} fullWidth maxWidth="md" onClose={() => { parseGeneration.current++; setParsing(false); setParseOpen(false) }}>
      <DialogTitle>Parse Command</DialogTitle><DialogContent>
        <Typography variant="body2">Paste one command. Quoting and escaped spaces are respected; commands are never executed and variables are never expanded.</Typography>
        <TextField fullWidth multiline minRows={3} label="Command" value={command} sx={{ mt: 2 }} onChange={event => { parseGeneration.current++; setParsing(false); setCommand(event.target.value); setPreview(null) }} />
        <TextField fullWidth label="Step name (optional)" value={stepName} sx={{ my: 2 }} onChange={event => { parseGeneration.current++; setParsing(false); setStepName(event.target.value); setPreview(null) }} />
        {parseError && <Alert severity="error">{parseError}</Alert>}
        {preview && <><pre className="parse-preview">{preview.yaml}</pre>{preview.warnings.map((warning, index) => <Alert key={index} severity="warning">{warning}</Alert>)}</>}
      </DialogContent><DialogActions>
        <Button onClick={() => { parseGeneration.current++; setParsing(false); setParseOpen(false) }}>Cancel</Button>
        <Button onClick={() => void parseCommand()} disabled={parsing || !command.trim()}>{parsing ? 'Parsing…' : 'Parse'}</Button>
        <Button disabled={!preview || !canParse} onClick={() => { if (selection && preview) { mutate(() => replaceEmptyStep(latestText.current, ['watches', selection.watch, 'pipeline', selection.step], preview.yaml)); setParseOpen(false) } }}>Use Step</Button>
      </DialogActions>
    </Dialog>
  </section></ThemeProvider>
}

type FieldKind = 'text' | 'multiline' | 'number' | 'boolean' | 'list' | 'map' | 'mounts'
function Fields({ value, onChange, fields }: { value: RecordValue; onChange: (key: string, value: unknown) => void; fields: Record<string, FieldKind> }) {
  return <Box className="pipeline-fields">{Object.entries(fields).map(([key, kind]) => <Field key={key} name={key} kind={kind} value={value[key]} onChange={next => onChange(key, next)} />)}</Box>
}
function Field({ name, kind, value, onChange }: { name: string; kind: FieldKind; value: unknown; onChange: (value: unknown) => void }) {
  const [newKey, setNewKey] = useState('')
  const label = name.replaceAll('_', ' ')
  if (kind === 'boolean') return <FormControlLabel control={<Switch checked={value === true} onChange={event => onChange(event.target.checked)} />} label={label} />
  if (kind === 'list' || kind === 'mounts') {
    const items = list(value)
    const change = (index: number, next: unknown) => onChange(items.map((item, i) => i === index ? next : item))
    return <Box sx={{ borderLeft: '2px solid', borderColor: 'divider', pl: 1 }}>
      <Typography variant="subtitle2">{label}</Typography>
      {value !== undefined && !Array.isArray(value) && <Alert severity="error">Expected a list. Correct this field in YAML.</Alert>}
      {items.map((item, index) => <Box key={index} sx={{ my: 1 }}>
        {kind === 'mounts' ? <Fields value={record(item)} fields={{ source: 'text', target: 'text', options: 'list', ...(record(item).read_only !== undefined ? { read_only: 'boolean' as const } : {}) }} onChange={(key, next) => { const updated = { ...record(item), [key]: next }; if (next === undefined) delete updated[key]; change(index, updated) }} />
          : <TextField fullWidth size="small" label={`${label} ${index + 1}`} value={String(item ?? '')} onChange={event => change(index, event.target.value)} />}
        <Button size="small" onClick={() => onChange(items.filter((_, i) => i !== index))}>Remove {kind === 'mounts' ? 'mount' : 'entry'} {index + 1}</Button>
      </Box>)}
      <Button size="small" onClick={() => onChange([...items, kind === 'mounts' ? { source: '', target: '' } : ''])}>Add {label}</Button>
      {value !== undefined && <Button size="small" onClick={() => onChange(undefined)}>Clear {label}</Button>}
    </Box>
  }
  if (kind === 'map') {
    const entries = record(value)
    return <Box sx={{ borderLeft: '2px solid', borderColor: 'divider', pl: 1 }}>
      <Typography variant="subtitle2">{label}</Typography>
      {value !== undefined && (value === null || typeof value !== 'object' || Array.isArray(value)) && <Alert severity="error">Expected a mapping. Correct this field in YAML.</Alert>}
      {Object.entries(entries).map(([key, item]) => <Stack key={key} direction="row" spacing={1} sx={{ my: 1 }}>
        <TextField fullWidth size="small" label={key} value={String(item ?? '')} onChange={event => onChange({ ...entries, [key]: event.target.value })} />
        <Button sx={{ 'min-width': '125px' }} onClick={() => { const next = { ...entries }; delete next[key]; onChange(next) }}>Remove {key}</Button>
      </Stack>)}
      <Stack direction="row" spacing={1} sx={{ mt: 1 }}><TextField fullWidth size="small" label={`New ${label} key`} value={newKey} onChange={event => setNewKey(event.target.value)} /><Button disabled={!newKey.trim() || Object.hasOwn(entries, newKey)} onClick={() => { onChange({ ...entries, [newKey]: '' }); setNewKey('') }}>Add key</Button></Stack>
      {value !== undefined && <Button size="small" onClick={() => onChange(undefined)}>Clear {label}</Button>}
    </Box>
  }
  return <TextField fullWidth size="small" label={label} value={value === undefined ? '' : String(value)} multiline={kind === 'multiline'} type={kind === 'number' ? 'number' : 'text'} onChange={event => onChange(event.target.value === '' ? undefined : kind === 'number' ? Number(event.target.value) : event.target.value)} />
}
