import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { APIError, api } from './api'
import {
  applyTheme,
  persistThemePreference,
  readThemePreference,
  syncThemeColor,
  themeStorageKey,
  type ThemePreference,
} from './theme'
import type {
  Command,
  CommandOutput,
  InfoResponse,
  Instance,
  JobResponse,
  JobsResponse,
  JobStatus,
  QueueCounts,
  QueueSummary,
} from './types'

const tokenKey = 'onderzeeer.web.token'
type OutputState =
  | { state: 'loading' }
  | { state: 'ready'; output: CommandOutput }
  | { state: 'error'; message: string }

export default function App() {
  const [token, setToken] = useState(() => sessionStorage.getItem(tokenKey) ?? '')
  const [authMessage, setAuthMessage] = useState('')
  const [access, setAccess] = useState<InfoResponse | null>(null)
  const [checkingAccess, setCheckingAccess] = useState(true)
  const [accessError, setAccessError] = useState('')
  const [accessAttempt, setAccessAttempt] = useState(0)
  const [unlocking, setUnlocking] = useState(false)
  const [theme, setTheme] = useThemePreference()
  const showDashboard = access !== null && !unlocking && !checkingAccess && !accessError

  useEffect(() => {
    syncThemeColor(showDashboard ? 'dashboard' : 'auth')
  }, [theme, showDashboard])

  const authenticate = (value: string) => {
    const normalized = value.trim()
    sessionStorage.setItem(tokenKey, normalized)
    setAuthMessage('')
    setAccess(null)
    setCheckingAccess(true)
    setUnlocking(false)
    setToken(normalized)
  }

  const signOut = useCallback((message = '') => {
    sessionStorage.removeItem(tokenKey)
    setAccess(null)
    setCheckingAccess(true)
    setUnlocking(false)
    setToken('')
    setAuthMessage(message)
    setAccessAttempt((attempt) => attempt + 1)
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    setCheckingAccess(true)
    setAccessError('')
    void api.info(token, controller.signal)
      .then((info) => {
        if (controller.signal.aborted) return
        setAccess(info)
        setCheckingAccess(false)
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return
        if (error instanceof APIError && error.status === 401) {
          if (token) {
            signOut('That token was not accepted. Check the token file and try again.')
            return
          }
          setAccess(null)
        } else {
          setAccessError(error instanceof Error ? error.message : 'Could not connect to onderzeeer')
        }
        setCheckingAccess(false)
      })
    return () => controller.abort()
  }, [token, accessAttempt, signOut])

  if (checkingAccess || accessError) {
    return (
      <main className="auth-shell">
        <ThemeControl className="auth-theme-control" theme={theme} onChange={setTheme} />
        <section className="auth-card" aria-live="polite">
          <img className="brand-mark" src="/icon.png" alt="" />
          <h1>{accessError ? 'Connection unavailable.' : 'Connecting…'}</h1>
          {accessError && <>
            <p className="form-error" role="alert">{accessError}</p>
            <button className="button button-primary" onClick={() => setAccessAttempt((attempt) => attempt + 1)}>Retry</button>
          </>}
        </section>
      </main>
    )
  }
  if (!access || unlocking) {
    return <TokenGate message={authMessage} theme={theme} onThemeChange={setTheme} onAuthenticate={authenticate}
      onCancel={access?.public_read ? () => setUnlocking(false) : undefined} />
  }
  return <Dashboard token={token} access={access} authMessage={authMessage} onDismissAuthMessage={() => setAuthMessage('')}
    theme={theme} onThemeChange={setTheme} onSignOut={signOut} onUnlock={() => setUnlocking(true)} />
}

function useThemePreference() {
  const [theme, setTheme] = useState<ThemePreference>(readThemePreference)

  useEffect(() => {
    if (typeof window.matchMedia !== 'function') {
      applyTheme(theme, false)
      return
    }
    const query = window.matchMedia('(prefers-color-scheme: dark)')
    const syncSystemTheme = () => applyTheme(theme, query.matches)
    syncSystemTheme()
    if (theme !== 'system') return
    query.addEventListener('change', syncSystemTheme)
    return () => query.removeEventListener('change', syncSystemTheme)
  }, [theme])

  useEffect(() => {
    const syncStoredTheme = (event: StorageEvent) => {
      if (event.key === themeStorageKey) setTheme(readThemePreference())
    }
    window.addEventListener('storage', syncStoredTheme)
    return () => window.removeEventListener('storage', syncStoredTheme)
  }, [])

  const updateTheme = useCallback((preference: ThemePreference) => {
    persistThemePreference(preference)
    setTheme(preference)
  }, [])

  return [theme, updateTheme] as const
}

function ThemeControl({ theme, onChange, className = '' }: { theme: ThemePreference; onChange: (theme: ThemePreference) => void; className?: string }) {
  return (
    <label className={`theme-control ${className}`.trim()}>
      <span>Theme</span>
      <select aria-label="Color theme" value={theme} onChange={(event) => onChange(event.target.value as ThemePreference)}>
        <option value="system">Auto</option>
        <option value="light">Light</option>
        <option value="dark">Night</option>
      </select>
    </label>
  )
}

function TokenGate({
  message,
  theme,
  onThemeChange,
  onAuthenticate,
  onCancel,
}: {
  message: string
  theme: ThemePreference
  onThemeChange: (theme: ThemePreference) => void
  onAuthenticate: (token: string) => void
  onCancel?: () => void
}) {
  const [value, setValue] = useState('')

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (value.trim()) onAuthenticate(value)
  }

  return (
    <main className="auth-shell">
      <ThemeControl className="auth-theme-control" theme={theme} onChange={onThemeChange} />
      <section className="auth-card" aria-labelledby="auth-title">
        <img className="brand-mark" src="/icon.png" alt="" />
        <p className="eyebrow">Local control plane</p>
        <h1 id="auth-title">{onCancel ? 'Unlock controls.' : 'Enter the control room.'}</h1>
        <p className="auth-copy">
          Paste the access token from the private token file shown in the <code>onderzeeerd</code> startup log.
          It stays in this browser tab only.
        </p>
        <form onSubmit={submit}>
          <label htmlFor="access-token">Web access token</label>
          <input
            id="access-token"
            type="password"
            autoComplete="off"
            spellCheck="false"
            value={value}
            onChange={(event) => setValue(event.target.value)}
            placeholder="Paste token"
            autoFocus
          />
          {message && <p className="form-error" role="alert">{message}</p>}
          <button className="button button-primary button-wide" type="submit" disabled={!value.trim()}>
            {onCancel ? 'Unlock controls' : 'Open dashboard'} <span aria-hidden="true">→</span>
          </button>
          {onCancel && <button className="button button-wide" type="button" onClick={onCancel}>Back to read-only view</button>}
        </form>
        <p className="auth-note">
          This token is sent with dashboard API requests. Plain HTTP does not protect it on an untrusted network.
        </p>
      </section>
      <div className="auth-grid" aria-hidden="true" />
    </main>
  )
}

function Dashboard({
  token,
  access,
  authMessage,
  onDismissAuthMessage,
  theme,
  onThemeChange,
  onSignOut,
  onUnlock,
}: {
  token: string
  access: InfoResponse
  authMessage: string
  onDismissAuthMessage: () => void
  theme: ThemePreference
  onThemeChange: (theme: ThemePreference) => void
  onSignOut: (message?: string) => void
  onUnlock: () => void
}) {
  const canControl = access.can_control
  const [queues, setQueues] = useState<QueueSummary[] | null>(null)
  const [instances, setInstances] = useState<Instance[] | null>(null)
  const [selectedQueueID, setSelectedQueueID] = useState('')
  const [status, setStatus] = useState<'' | JobStatus>('')
  const [watch, setWatch] = useState('')
  const [search, setSearch] = useState('')
  const [searchFilter, setSearchFilter] = useState('')
  const [offset, setOffset] = useState(0)
  const [pageSize, setPageSize] = useState(50)
  const [instanceQueueID, setInstanceQueueID] = useState('')
  const [jobs, setJobs] = useState<JobsResponse | null>(null)
  const [selectedJobID, setSelectedJobID] = useState<number | null>(null)
  const [jobDetail, setJobDetail] = useState<JobResponse | null>(null)
  const [outputs, setOutputs] = useState<Record<number, OutputState>>({})
  const [refreshError, setRefreshError] = useState('')
  const [jobsError, setJobsError] = useState('')
  const [detailError, setDetailError] = useState('')
  const [actionError, setActionError] = useState('')
  const [actionID, setActionID] = useState('')
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null)
  const overviewController = useRef<AbortController | null>(null)
  const jobsController = useRef<AbortController | null>(null)
  const jobsRequestKey = useRef('')
  const detailController = useRef<AbortController | null>(null)
  const detailRequestKey = useRef('')
  const outputControllers = useRef(new Map<number, AbortController>())

  const handleError = useCallback((error: unknown, fallback: string) => {
    if (error instanceof APIError && error.status === 401) {
      onSignOut('That token was not accepted. Check the token file and try again.')
      return ''
    }
    return error instanceof Error ? error.message : fallback
  }, [onSignOut])

  const refreshOverview = useCallback(async () => {
    if (overviewController.current) return
    const controller = new AbortController()
    overviewController.current = controller
    try {
      const [queueResult, instanceResult] = await Promise.all([
        api.queues(token, controller.signal),
        api.instances(token, controller.signal),
      ])
      if (controller.signal.aborted) return
      setQueues(queueResult.queues)
      setInstances(instanceResult.instances)
      setLastUpdated(new Date(queueResult.generated_at))
      setRefreshError('')
    } catch (error) {
      if (controller.signal.aborted) return
      const message = handleError(error, 'Could not refresh onderzeeer')
      if (message) setRefreshError(message)
    } finally {
      if (overviewController.current === controller) overviewController.current = null
    }
  }, [handleError, token])

  useEffect(() => {
    void refreshOverview()
    const refresh = () => {
      if (!document.hidden) void refreshOverview()
    }
    const interval = window.setInterval(refresh, 4000)
    document.addEventListener('visibilitychange', refresh)
    window.addEventListener('focus', refresh)
    return () => {
      window.clearInterval(interval)
      document.removeEventListener('visibilitychange', refresh)
      window.removeEventListener('focus', refresh)
    }
  }, [refreshOverview])

  useEffect(() => {
    if (!queues?.length) {
      setSelectedQueueID('')
      return
    }
    if (!queues.some((queue) => queue.id === selectedQueueID)) {
      setSelectedQueueID(queues[0].id)
    }
  }, [queues, selectedQueueID])

  const selectedQueue = queues?.find((queue) => queue.id === selectedQueueID) ?? null
  const instanceQueue = queues?.find((queue) => queue.id === instanceQueueID) ?? null

  useEffect(() => {
    const timer = window.setTimeout(() => setSearchFilter(search.trim()), 250)
    return () => window.clearTimeout(timer)
  }, [search])

  const refreshJobs = useCallback(async () => {
    if (!selectedQueueID || selectedQueue?.database_state !== 'ready' || search.trim() !== searchFilter) {
      jobsController.current?.abort()
      jobsController.current = null
      jobsRequestKey.current = ''
      setJobs(null)
      return
    }
    const requestKey = JSON.stringify([selectedQueueID, status, watch, searchFilter, offset, pageSize])
    if (jobsController.current) {
      if (!jobsController.current.signal.aborted && jobsRequestKey.current === requestKey) return
      jobsController.current.abort()
    }
    const controller = new AbortController()
    jobsController.current = controller
    jobsRequestKey.current = requestKey
    try {
      const result = await api.jobs(token, selectedQueueID, status, watch, searchFilter, offset, pageSize, controller.signal)
      if (controller.signal.aborted) return
      setJobs(result)
      setJobsError('')
    } catch (error) {
      if (controller.signal.aborted) return
      const message = handleError(error, 'Could not refresh jobs')
      if (message) setJobsError(message)
    } finally {
      if (jobsController.current === controller) {
        jobsController.current = null
        jobsRequestKey.current = ''
      }
    }
  }, [handleError, offset, pageSize, search, searchFilter, selectedQueue?.database_state, selectedQueueID, status, token, watch])

  useEffect(() => {
    setJobs(null)
    setJobsError('')
    void refreshJobs()
    const interval = window.setInterval(() => {
      if (!document.hidden) void refreshJobs()
    }, 4000)
    return () => window.clearInterval(interval)
  }, [refreshJobs])

  const refreshDetail = useCallback(async () => {
    if (!selectedQueueID || selectedJobID === null) {
      detailController.current?.abort()
      detailController.current = null
      detailRequestKey.current = ''
      return
    }
    const requestKey = `${selectedQueueID}\u0000${selectedJobID}`
    if (detailController.current) {
      if (detailRequestKey.current === requestKey) return
      detailController.current.abort()
    }
    const controller = new AbortController()
    detailController.current = controller
    detailRequestKey.current = requestKey
    try {
      const detail = await api.job(token, selectedQueueID, selectedJobID, controller.signal)
      if (controller.signal.aborted) return
      setJobDetail(detail)
      setDetailError('')
    } catch (error) {
      if (controller.signal.aborted) return
      const message = handleError(error, 'Could not load job detail')
      if (message) setDetailError(message)
    } finally {
      if (detailController.current === controller) {
        detailController.current = null
        detailRequestKey.current = ''
      }
    }
  }, [handleError, selectedJobID, selectedQueueID, token])

  useEffect(() => {
    detailController.current?.abort()
    detailController.current = null
    detailRequestKey.current = ''
    for (const controller of outputControllers.current.values()) controller.abort()
    outputControllers.current.clear()
    setJobDetail(null)
    setOutputs({})
    setDetailError('')
    if (selectedJobID === null) return
    void refreshDetail()
  }, [refreshDetail, selectedJobID, selectedQueueID])

  useEffect(() => {
    if (selectedJobID === null || !jobDetail || !['QUEUED', 'PENDING', 'RUNNING'].includes(jobDetail.job.status)) return
    const interval = window.setInterval(() => {
      if (!document.hidden) void refreshDetail()
    }, 2500)
    return () => window.clearInterval(interval)
  }, [jobDetail, refreshDetail, selectedJobID])

  useEffect(() => () => {
    overviewController.current?.abort()
    jobsController.current?.abort()
    detailController.current?.abort()
    for (const controller of outputControllers.current.values()) controller.abort()
  }, [])

  const totals = useMemo(() => {
    const result: QueueCounts = { total: 0, queued: 0, pending: 0, running: 0, succeeded: 0, failed: 0 }
    for (const queue of queues ?? []) {
      result.total += queue.counts.total
      result.queued += queue.counts.queued
      result.pending += queue.counts.pending
      result.running += queue.counts.running
      result.succeeded += queue.counts.succeeded
      result.failed += queue.counts.failed
    }
    return result
  }, [queues])

  const changeQueue = (id: string) => {
    jobsController.current?.abort()
    jobsController.current = null
    jobsRequestKey.current = ''
    setSelectedQueueID(id)
    setStatus('')
    setWatch('')
    setSearch('')
    setSearchFilter('')
    setOffset(0)
    setJobs(null)
    setJobsError('')
    setSelectedJobID(null)
  }

  const changeStatus = (nextStatus: '' | JobStatus) => {
    jobsController.current?.abort()
    setJobs(null)
    setJobsError('')
    setStatus(nextStatus)
    setOffset(0)
  }

  const changeWatch = (nextWatch: string) => {
    if (nextWatch === watch) return
    jobsController.current?.abort()
    setJobs(null)
    setJobsError('')
    setWatch(nextWatch)
    setOffset(0)
  }

  const changeSearch = (nextSearch: string) => {
    jobsController.current?.abort()
    setJobs(null)
    setJobsError('')
    setSearch(nextSearch)
    setOffset(0)
  }

  const changeOffset = (nextOffset: number) => {
    jobsController.current?.abort()
    setJobs(null)
    setJobsError('')
    setOffset(nextOffset)
  }

  const changePageSize = (nextSize: number) => {
    jobsController.current?.abort()
    setJobs(null)
    setJobsError('')
    setPageSize(nextSize)
    setOffset(0)
  }

  const performAction = async (kind: 'start' | 'stop', id: string) => {
    if (!canControl) return
    if (kind === 'stop' && !window.confirm('Stop this onderzeeer instance gracefully?')) return
    setActionID(id)
    setActionError('')
    try {
      if (kind === 'start') await api.start(token, id)
      else await api.stop(token, id)
      await refreshOverview()
      await refreshJobs()
    } catch (error) {
      const message = handleError(error, `Could not ${kind} instance`)
      if (message) setActionError(message)
    } finally {
      setActionID('')
    }
  }

  const loadOutput = async (commandID: number) => {
    if (!selectedQueueID || !jobDetail || jobDetail.job.id !== selectedJobID || !isFinished(jobDetail.job.status)) return
    const command = jobDetail.runs.flatMap((run) => run.commands).find((item) => item.id === commandID)
    if (!command || !isFinished(command.status)) return
    outputControllers.current.get(commandID)?.abort()
    const controller = new AbortController()
    outputControllers.current.set(commandID, controller)
    setOutputs((current) => ({ ...current, [commandID]: { state: 'loading' } }))
    try {
      const output = await api.output(token, selectedQueueID, commandID, controller.signal)
      if (controller.signal.aborted) return
      setOutputs((current) => ({ ...current, [commandID]: { state: 'ready', output } }))
    } catch (error) {
      if (controller.signal.aborted) return
      const message = handleError(error, 'Could not load output')
      if (message) setOutputs((current) => ({ ...current, [commandID]: { state: 'error', message } }))
    } finally {
      if (outputControllers.current.get(commandID) === controller) outputControllers.current.delete(commandID)
    }
  }

  const closeJob = useCallback(() => setSelectedJobID(null), [])
  const closeInstances = useCallback(() => setInstanceQueueID(''), [])

  return (
    <div className="app-shell">
      <header className="topbar">
        <div className="brand-lockup">
          <img className="brand-mark brand-mark-small" src="/icon.png" alt="" />
          <div>
            <strong>onderzeeer</strong>
            <span className="daemon-version">Version {access.version || 'unavailable'}</span>
          </div>
        </div>
        <div className="topbar-actions">
          <ThemeControl theme={theme} onChange={onThemeChange} />
          <span className={`connection-state ${refreshError ? 'offline' : lastUpdated ? 'online' : 'connecting'}`}>
            <i /> {refreshError ? 'Refresh failed' : lastUpdated ? 'Local daemon' : 'Connecting'}
          </span>
          {!canControl && <span className="read-only-badge">Read only</span>}
          {canControl
            ? <button className="text-button" onClick={() => onSignOut()}>Lock</button>
            : <button className="text-button" onClick={onUnlock}>Unlock controls</button>}
        </div>
      </header>

      {authMessage && <div className="notice notice-error" role="alert">
        <span>{authMessage}</span>
        <button onClick={onDismissAuthMessage}>Dismiss</button>
      </div>}

      {(refreshError || actionError) && (
        <div className="notice notice-error" role="alert">
          <span>{actionError || refreshError}</span>
          {actionError
            ? <button onClick={() => setActionError('')}>Dismiss</button>
            : <button onClick={() => void refreshOverview()}>Retry</button>}
        </div>
      )}

      <main className="workspace">
        <section className="page-intro">
          <div>
            <p className="eyebrow">Durable work</p>
            <h1>Every queue, one view.</h1>
          </div>
          <div className="update-block">
            <span>Last sync</span>
            <strong>{lastUpdated ? formatClock(lastUpdated.toISOString()) : 'Connecting…'}</strong>
            <button className="icon-button" onClick={() => void refreshOverview()} aria-label="Refresh dashboard">↻</button>
          </div>
        </section>

        <section className="metrics" aria-label="Queue totals">
          <Metric label="All jobs" value={totals.total} tone="ink" />
          <Metric label="Queued" value={totals.queued} tone="queued" />
          <Metric label="Pending" value={totals.pending} tone="pending" />
          <Metric label="Running" value={totals.running} tone="running" />
          <Metric label="Failed" value={totals.failed} tone="failed" />
        </section>

        {queues === null ? (
          <DashboardSkeleton />
        ) : queues.length === 0 ? (
          <EmptyState title="No queues registered" copy="Register a config with onderzeeer start. Its queue and instance will remain available after daemon restarts." />
        ) : (
          <section className="queue-workspace">
            <QueueRail
              queues={queues}
              selectedID={selectedQueueID}
              actionID={actionID}
              canControl={canControl}
              onSelect={changeQueue}
              onAction={performAction}
              onInstances={(id) => { setSelectedJobID(null); setInstanceQueueID(id) }}
            />
            {selectedQueue && (
              <QueuePanel
                queue={selectedQueue}
                jobs={jobs}
                error={jobsError}
                status={status}
                watch={watch}
                search={search}
                offset={offset}
                pageSize={pageSize}
                onStatus={changeStatus}
                onWatch={changeWatch}
                onSearch={changeSearch}
                onOffset={changeOffset}
                onPageSize={changePageSize}
                onRefresh={() => void refreshJobs()}
                onSelectJob={setSelectedJobID}
              />
            )}
          </section>
        )}
      </main>

      {instanceQueue && (
        <InstanceDrawer
          queue={instanceQueue}
          instances={instances?.filter((instance) => instance.database_path === instanceQueue.database_path || instance.config_path === instanceQueue.config_path) ?? null}
          actionID={actionID}
          canControl={canControl}
          actionError={actionError}
          onStop={(id) => void performAction('stop', id)}
          onClose={closeInstances}
        />
      )}

      {selectedJobID !== null && selectedQueue && (
        <JobDrawer
          key={`${selectedQueue.id}:${selectedJobID}`}
          queue={selectedQueue}
          detail={jobDetail}
          error={detailError}
          outputs={outputs}
          onLoadOutput={(id) => void loadOutput(id)}
          onClose={closeJob}
        />
      )}
    </div>
  )
}

function QueueRail({
  queues,
  selectedID,
  actionID,
  canControl,
  onSelect,
  onAction,
  onInstances,
}: {
  queues: QueueSummary[]
  selectedID: string
  actionID: string
  canControl: boolean
  onSelect: (id: string) => void
  onAction: (kind: 'start' | 'stop', id: string) => void
  onInstances: (id: string) => void
}) {
  return (
    <aside className="queue-rail" aria-label="Configured queues">
      <div className="section-label"><span>Configured queues</span><b>{queues.length}</b></div>
      <div className="queue-list">
        {queues.map((queue) => {
          const active = Boolean(queue.active_instance)
          const actionKey = active ? queue.active_instance!.id : queue.id
          const instanceState = queue.active_instance?.state
          const stateCopy = instanceState
            ? `Instance ${instanceState}`
            : queue.database_state === 'ready'
              ? 'Stopped · history available'
              : queue.database_state === 'missing'
                ? 'Queue not initialized'
                : 'Queue unavailable'
          return (
            <article className={`queue-card ${selectedID === queue.id ? 'selected' : ''}`} key={queue.id}>
              <button className="queue-select" onClick={() => onSelect(queue.id)} aria-pressed={selectedID === queue.id}>
                <span className={`queue-orb ${active ? 'live' : ''}`} aria-hidden="true" />
                <span className="queue-card-copy">
                  <strong>{queue.display_name}</strong>
                  <small>{stateCopy}</small>
                </span>
                <span className="queue-total">{queue.counts.total}</span>
              </button>
              <div className="queue-card-actions">
                <button className="mini-action" onClick={() => onInstances(queue.id)} aria-label={`View instance information for ${queue.display_name}`} title="Instance information">ⓘ</button>
                {canControl && <button
                className={`mini-action ${active ? 'stop' : ''}`}
                onClick={() => onAction(active ? 'stop' : 'start', actionKey)}
                disabled={Boolean(actionID) || instanceState === 'stopping'}
                aria-label={`${active ? 'Stop' : 'Start'} ${queue.display_name}`}
              >
                {actionID === actionKey ? '…' : instanceState === 'stopping' ? '…' : active ? '■' : '▶'}
                </button>}
              </div>
            </article>
          )
        })}
      </div>
    </aside>
  )
}

function QueuePanel({
  queue,
  jobs,
  error,
  status,
  watch,
  search,
  offset,
  pageSize,
  onStatus,
  onWatch,
  onSearch,
  onOffset,
  onPageSize,
  onRefresh,
  onSelectJob,
}: {
  queue: QueueSummary
  jobs: JobsResponse | null
  error: string
  status: '' | JobStatus
  watch: string
  search: string
  offset: number
  pageSize: number
  onStatus: (status: '' | JobStatus) => void
  onWatch: (watch: string) => void
  onSearch: (search: string) => void
  onOffset: (offset: number) => void
  onPageSize: (size: number) => void
  onRefresh: () => void
  onSelectJob: (id: number) => void
}) {
  return (
    <section className="queue-panel" aria-labelledby="queue-title">
      <header className="queue-header">
        <div className="queue-title-line">
          <h2 id="queue-title">{queue.display_name}</h2>
          <StatusPill status={queue.active_instance?.state ?? 'stopped'} />
        </div>
        <div className="queue-tools">
          <button className="icon-button" onClick={onRefresh} aria-label="Refresh jobs">↻</button>
        </div>
        <div className="queue-watch-folders">
          <span className="queue-path-label">{queue.watches.length === 1 ? 'Watch folder' : 'Watch folders'}</span>
          {queue.watches.map((source) => (
            <div className="queue-watch-folder" key={source.name}>
              {queue.watches.length > 1 && <span className="watch-tag">{source.name}</span>}
              <code title={source.path}>{source.path}</code>
            </div>
          ))}
        </div>
        <div className="queue-search">
          {(watch || status) && (
            <div className="job-filter-tags" aria-label="Active job filters">
              {status && <button className="job-filter-tag" onClick={() => onStatus('')} aria-label={`Remove status filter ${status}`}><span>Status: {status.toLowerCase()}</span><span aria-hidden="true">×</span></button>}
              {watch && <button className="job-filter-tag" onClick={() => onWatch('')} aria-label={`Remove watch filter ${watch}`}><span>Watch: {watch}</span><span aria-hidden="true">×</span></button>}
            </div>
          )}
          <input type="search" aria-label="Search jobs" placeholder="Search jobs…" maxLength={1024} value={search} onChange={(event) => onSearch(event.target.value)} title="Search file paths, watch names, job IDs, statuses, and errors" />
        </div>
      </header>

      <div className="queue-count-strip">
        <CountButton label="All" value={queue.counts.total} active={!status} onClick={() => onStatus('')} />
        <CountButton label="Queued" value={queue.counts.queued} active={status === 'QUEUED'} onClick={() => onStatus('QUEUED')} />
        <CountButton label="Pending" value={queue.counts.pending} active={status === 'PENDING'} onClick={() => onStatus('PENDING')} />
        <CountButton label="Running" value={queue.counts.running} active={status === 'RUNNING'} onClick={() => onStatus('RUNNING')} />
        <CountButton label="Succeeded" value={queue.counts.succeeded} active={status === 'SUCCEEDED'} onClick={() => onStatus('SUCCEEDED')} />
        <CountButton label="Failed" value={queue.counts.failed} active={status === 'FAILED'} onClick={() => onStatus('FAILED')} />
      </div>

      {queue.database_state !== 'ready' ? (
        <EmptyState
          title={queue.database_state === 'missing' ? 'Queue not created yet' : 'Queue unavailable'}
          copy={queue.error ?? 'Start the instance to initialize this queue.'}
          compact
        />
      ) : error && !jobs ? (
        <EmptyState title="Could not read this queue" copy={error} compact />
      ) : jobs === null ? (
        <TableSkeleton />
      ) : jobs.jobs.length === 0 ? (
        <>
          <EmptyState
            title={status || watch || search.trim() ? 'No matching jobs' : 'No jobs recorded'}
            copy={offset > 0 ? 'This page is now empty; return to newer jobs.' : status || watch || search.trim() ? 'Try another search or remove a filter.' : 'Matching files will appear here when they are discovered.'}
            compact
          />
        </>
      ) : (
        <>
          {error && <p className="inline-warning">Showing the last result: {error}</p>}
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Status</th>
                  <th>Job</th>
                  <th>Watch</th>
                  <th>Attempts</th>
                  <th>Updated</th>
                </tr>
              </thead>
              <tbody>
                {jobs.jobs.map((job) => (
                  <tr key={job.id}>
                    <td><StatusPill status={job.status} /></td>
                    <td>
                      <button className="job-link" onClick={() => onSelectJob(job.id)}>
                        <strong>{fileName(job.path)}</strong>
                        <small title={job.path}>#{job.id} · {job.path}</small>
                        {job.status === 'QUEUED' && job.last_error && (
                          <small className="retry-note" title={job.last_error}>Retry {relativeTime(job.available_at)} · {job.last_error}</small>
                        )}
                      </button>
                    </td>
                    <td><button className="watch-tag watch-filter" aria-label={`Filter by watch ${job.watch_name}`} aria-pressed={watch === job.watch_name} onClick={() => onWatch(job.watch_name)}>{job.watch_name}</button></td>
                    <td>{job.attempts}<span className="muted"> / {job.max_retries + 1}</span></td>
                    <td>{relativeTime(job.updated_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
      <footer className="pagination">
        <label>
          <span>Jobs per page</span>
          <select value={pageSize} onChange={(event) => onPageSize(Number(event.target.value))}>
            {[10, 25, 50, 100].map((size) => <option key={size} value={size}>{size}</option>)}
          </select>
        </label>
        <div className="pagination-navigation">
          <span>{jobs ? jobs.jobs.length ? `${offset + 1}–${offset + jobs.jobs.length}` : 'No jobs' : '—'}</span>
          <button disabled={offset === 0 || !jobs} onClick={() => onOffset(Math.max(0, offset - pageSize))}>← Newer</button>
          <button disabled={!jobs?.has_more} onClick={() => onOffset(offset + pageSize)}>Older →</button>
        </div>
      </footer>
    </section>
  )
}

function useDrawer(onClose: () => void) {
  const dialogRef = useRef<HTMLElement>(null)
  const closeRef = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    const previouslyFocused = document.activeElement instanceof HTMLElement ? document.activeElement : null
    const previousOverflow = document.body.style.overflow
    const background = Array.from(document.querySelectorAll<HTMLElement>('.topbar, .workspace, .notice'))
    const priorInert = background.map((element) => element.inert)
    document.body.style.overflow = 'hidden'
    background.forEach((element) => { element.inert = true })
    closeRef.current?.focus()

    const keydown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault()
        onClose()
        return
      }
      if (event.key !== 'Tab' || !dialogRef.current) return
      const focusable = Array.from(dialogRef.current.querySelectorAll<HTMLElement>(
        'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
      ))
      if (focusable.length === 0) {
        event.preventDefault()
        return
      }
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault()
        first.focus()
      }
    }
    document.addEventListener('keydown', keydown)
    return () => {
      document.removeEventListener('keydown', keydown)
      document.body.style.overflow = previousOverflow
      background.forEach((element, index) => { element.inert = priorInert[index] })
      previouslyFocused?.focus()
    }
  }, [onClose])
  return { dialogRef, closeRef }
}

function InstanceDrawer({ queue, instances, actionID, canControl, actionError, onStop, onClose }: {
  queue: QueueSummary
  instances: Instance[] | null
  actionID: string
  canControl: boolean
  actionError: string
  onStop: (id: string) => void
  onClose: () => void
}) {
  const { dialogRef, closeRef } = useDrawer(onClose)
  return (
    <div className="drawer-backdrop drawer-backdrop-left" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <aside ref={dialogRef} className="instance-drawer" role="dialog" aria-modal="true" aria-labelledby="instance-detail-title">
        <header className="drawer-header">
          <div>
            <p className="eyebrow">Instance information</p>
            <h2 id="instance-detail-title">{queue.display_name}</h2>
          </div>
          <button ref={closeRef} className="close-button" onClick={onClose} aria-label="Close instance information">×</button>
        </header>
        <div className="drawer-body">
          {actionError && <p className="inline-warning" role="alert">{actionError}</p>}
          {instances === null ? <DetailSkeleton /> : instances.length === 0 ? (
            <EmptyState title="No instance history" copy="Start this queue to create an instance." compact />
          ) : instances.map((instance) => (
            <section className="instance-summary" key={instance.id}>
              <div className="instance-summary-title"><h3>{instance.name}</h3><StatusPill status={instance.state} /></div>
              <dl>
                <Detail label="Instance ID" value={instance.id} />
                <Detail label="Desired state" value={instance.desired_state ?? '—'} />
                <Detail label="Created" value={formatDate(instance.created_at)} />
                <Detail label="Started" value={formatDate(instance.started_at)} />
                <Detail label="Finished" value={formatDate(instance.finished_at)} />
                <Detail label="Duration" value={duration(instance.started_at, instance.finished_at)} />
              </dl>
              <div className="path-block"><span>Config</span><code>{instance.config_path}</code></div>
              <div className="path-block"><span>Config hash</span><code>{instance.config_hash}</code></div>
              <div className="path-block"><span>Database</span><code>{instance.database_path}</code></div>
              {instance.error && <div className="error-block"><strong>Instance error</strong><pre>{instance.error}</pre></div>}
              {canControl && (['running', 'stopping'].includes(instance.state) || instance.desired_state === 'running') && (
                <button className="button button-danger button-small" disabled={Boolean(actionID) || instance.state === 'stopping'} onClick={() => onStop(instance.id)}>
                  {instance.state === 'stopping' ? 'Stopping' : actionID === instance.id ? 'Working…' : 'Stop instance'}
                </button>
              )}
            </section>
          ))}
          <p className="retention-note">Instances resume after daemon restarts unless explicitly stopped. Queue and execution history remain durable.</p>
        </div>
      </aside>
    </div>
  )
}

function JobDrawer({
  queue,
  detail,
  error,
  outputs,
  onLoadOutput,
  onClose,
}: {
  queue: QueueSummary
  detail: JobResponse | null
  error: string
  outputs: Record<number, OutputState>
  onLoadOutput: (commandID: number) => void
  onClose: () => void
}) {
  const { dialogRef, closeRef } = useDrawer(onClose)
  const initializedRuns = useRef(false)
  const [expandedRuns, setExpandedRuns] = useState<Set<number>>(() => new Set())

  useEffect(() => {
    if (!detail || initializedRuns.current || detail.runs.length === 0) return
    initializedRuns.current = true
    setExpandedRuns(new Set([detail.runs[detail.runs.length - 1].id]))
  }, [detail])



  return (
    <div className="drawer-backdrop" role="presentation" onMouseDown={(event) => event.target === event.currentTarget && onClose()}>
      <aside ref={dialogRef} className="job-drawer" role="dialog" aria-modal="true" aria-labelledby="job-detail-title">
        <header className="drawer-header">
          <div>
            <p className="eyebrow">{queue.display_name} / job detail</p>
            <h2 id="job-detail-title">{detail ? fileName(detail.job.path) : 'Loading job…'}</h2>
          </div>
          <button ref={closeRef} className="close-button" onClick={onClose} aria-label="Close job detail">×</button>
        </header>
        <div className="drawer-body">
          {error && !detail ? (
            <EmptyState title="Could not load job" copy={error} compact />
          ) : detail === null ? (
            <DetailSkeleton />
          ) : (
            <>
              {error && <p className="inline-warning">Showing the last job detail: {error}</p>}
              <section className="job-summary">
                <div className="job-summary-title">
                  <StatusPill status={detail.job.status} />
                  <code>Job #{detail.job.id}</code>
                </div>
                <dl>
                  <Detail label="Watch" value={detail.job.watch_name} />
                  <Detail label="Attempts" value={`${detail.job.attempts} / ${detail.job.max_retries + 1}`} />
                  <Detail label="Created" value={formatDate(detail.job.created_at)} />
                  <Detail label="Updated" value={formatDate(detail.job.updated_at)} />
                  <Detail label="Available" value={formatDate(detail.job.available_at)} />
                  <Detail label="Started" value={formatDate(detail.job.started_at)} />
                  <Detail label="Finished" value={formatDate(detail.job.finished_at)} />
                </dl>
                <div className="path-block"><span>Source file</span><code>{detail.job.path}</code></div>
                <div className="path-block"><span>Fingerprint</span><code>{detail.job.fingerprint || '—'}</code></div>
                {detail.job.last_error && <div className="error-block"><strong>Last error</strong><pre>{detail.job.last_error}</pre></div>}
              </section>

              <section className="runs-section">
                <div className="section-label"><span>Attempts</span><b>{detail.runs.length}</b></div>
                {detail.runs.length === 0 ? (
                  <EmptyState title="Waiting for first attempt" copy="This job has not been claimed by a worker yet." compact />
                ) : detail.runs.map((run) => (
                  <details
                    className="run-card"
                    key={run.id}
                    open={expandedRuns.has(run.id)}
                    onToggle={(event) => {
                      const runID = run.id
                      const open = event.currentTarget.open
                      setExpandedRuns((current) => {
                        const next = new Set(current)
                        if (open) next.add(runID)
                        else next.delete(runID)
                        return next
                      })
                    }}
                  >
                    <summary>
                      <span><b>Attempt {run.attempt}</b><small>{formatDate(run.started_at)}</small></span>
                      <StatusPill status={run.status} />
                    </summary>
                    <div className="run-body">
                      {run.error && <div className="error-block"><strong>Attempt error</strong><pre>{run.error}</pre></div>}
                      {run.commands.map((command) => (
                        <CommandCard
                          key={command.id}
                          command={command}
                          canLoadOutput={isFinished(detail.job.status) && isFinished(command.status)}
                          outputState={outputs[command.id]}
                          onLoadOutput={() => onLoadOutput(command.id)}
                        />
                      ))}
                    </div>
                  </details>
                ))}
              </section>
            </>
          )}
        </div>
      </aside>
    </div>
  )
}

function CommandCard({ command, canLoadOutput, outputState, onLoadOutput }: { command: Command; canLoadOutput: boolean; outputState?: OutputState; onLoadOutput: () => void }) {
  const invocation = [command.program, ...command.args].map(shellToken).join(' ')
  return (
    <article className="command-card">
      <header>
        <div><span className="step-number">{command.sequence}</span><strong>{command.name || command.program}</strong></div>
        <StatusPill status={command.status} />
      </header>
      <pre className="invocation">{invocation}</pre>
      <dl>
        <Detail label="Exit" value={command.exit_code === undefined ? '—' : String(command.exit_code)} />
        <Detail label="Timeout" value={command.timeout} />
        <Detail label="Duration" value={duration(command.started_at, command.finished_at)} />
      </dl>
      {command.working_directory && <p className="working-dir" title={command.working_directory}>cwd · {command.working_directory}</p>}
      {command.error && <div className="error-block"><strong>Command error</strong><pre>{command.error}</pre></div>}
      <div className="output-heading">
        <span>Captured output · {formatBytes(command.stdout_bytes + command.stderr_bytes)}</span>
        {!outputState && <button disabled={!canLoadOutput} title={!canLoadOutput ? 'Output is available when the job has failed or succeeded.' : undefined} onClick={onLoadOutput}>Load output</button>}
        {outputState?.state === 'loading' && <span>Loading…</span>}
        {outputState?.state === 'error' && <button disabled={!canLoadOutput} onClick={onLoadOutput}>Retry</button>}
        {outputState?.state === 'ready' && <button disabled={!canLoadOutput} onClick={onLoadOutput}>Reload output</button>}
      </div>
      {outputState?.state === 'error' && <p className="inline-warning">{outputState.message}</p>}
      {outputState?.state === 'ready' && (
        <div className="output-grid">
          <OutputBlock label="stdout" value={outputState.output.stdout} />
          <OutputBlock label="stderr" value={outputState.output.stderr} />
        </div>
      )}
    </article>
  )
}

function OutputBlock({ label, value }: { label: string; value: string }) {
  const outputRef = useRef<HTMLPreElement>(null)
  const [copyState, setCopyState] = useState<'idle' | 'copying' | 'copied' | 'error'>('idle')

  useEffect(() => {
    if (copyState !== 'copied') return
    const timer = window.setTimeout(() => setCopyState('idle'), 2000)
    return () => window.clearTimeout(timer)
  }, [copyState])

  const copyOutput = async () => {
    if (!value || copyState === 'copying') return
    setCopyState('copying')
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(value)
      } else {
        // Remote HTTP dashboards may not have access to the Clipboard API.
        const selection = window.getSelection()
        if (!selection || !outputRef.current) throw new Error('Copy unavailable')
        const previousRanges = Array.from({ length: selection.rangeCount }, (_, index) => selection.getRangeAt(index).cloneRange())
        const range = document.createRange()
        range.selectNodeContents(outputRef.current)
        try {
          selection.removeAllRanges()
          selection.addRange(range)
          if (!document.execCommand('copy')) throw new Error('Copy failed')
        } finally {
          selection.removeAllRanges()
          previousRanges.forEach((previousRange) => selection.addRange(previousRange))
        }
      }
      setCopyState('copied')
    } catch {
      setCopyState('error')
    }
  }

  return (
    <div className="output-block">
      <div className="output-block-heading">
        <span>{label}</span>
        <div className="output-copy-actions">
          <span role="status">{copyState === 'copied' ? 'Copied!' : ''}</span>
          <button type="button" onClick={() => void copyOutput()} disabled={!value || copyState === 'copying'} aria-label={`Copy ${label} to clipboard`}>
            {copyState === 'copying' ? 'Copying…' : 'Copy'}
          </button>
        </div>
      </div>
      {copyState === 'error' && <p className="inline-warning" role="alert">Could not copy. Select the output and copy it manually.</p>}
      <pre ref={outputRef}>{value || 'No output captured.'}</pre>
    </div>
  )
}

function Metric({ label, value, tone }: { label: string; value: number; tone: string }) {
  return <article className={`metric metric-${tone}`}><span>{label}</span><strong>{value.toLocaleString()}</strong><i /></article>
}

function CountButton({ label, value, active, onClick }: { label: string; value: number; active: boolean; onClick: () => void }) {
  return <button className={active ? 'active' : ''} aria-pressed={active} onClick={onClick}><span>{label}</span><strong>{value.toLocaleString()}</strong></button>
}

function StatusPill({ status }: { status: string }) {
  const normalized = status.toLowerCase()
  return <span className={`status status-${normalized}`} title={normalized === 'pending' ? 'Waiting for a resource' : undefined}><i />{normalized}</span>
}

function isFinished(status: string) {
  return status === 'SUCCEEDED' || status === 'FAILED'
}

function Detail({ label, value }: { label: string; value: ReactNode }) {
  return <div><dt>{label}</dt><dd>{value}</dd></div>
}

function EmptyState({ title, copy, compact = false }: { title: string; copy: string; compact?: boolean }) {
  return <section className={`empty-state ${compact ? 'compact' : ''}`}><span aria-hidden="true">∅</span><h2>{title}</h2><p>{copy}</p></section>
}

function DashboardSkeleton() {
  return <div className="queue-workspace"><div className="skeleton skeleton-rail" /><div className="skeleton skeleton-panel" /></div>
}

function TableSkeleton() {
  return <div className="table-skeleton" aria-label="Loading"><i /><i /><i /><i /></div>
}

function DetailSkeleton() {
  return <div className="detail-skeleton"><i /><i /><i /></div>
}

function fileName(path: string) {
  return path.split(/[\\/]/).pop() || path
}

function formatClock(value: string) {
  return new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' }).format(new Date(value))
}

function formatDate(value?: string) {
  if (!value) return '—'
  return new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' }).format(new Date(value))
}

function relativeTime(value: string) {
  const seconds = Math.round((new Date(value).getTime() - Date.now()) / 1000)
  const formatter = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' })
  if (Math.abs(seconds) < 60) return formatter.format(seconds, 'second')
  const minutes = Math.round(seconds / 60)
  if (Math.abs(minutes) < 60) return formatter.format(minutes, 'minute')
  const hours = Math.round(minutes / 60)
  if (Math.abs(hours) < 24) return formatter.format(hours, 'hour')
  return formatter.format(Math.round(hours / 24), 'day')
}

function duration(start: string, finish?: string) {
  const milliseconds = (finish ? new Date(finish).getTime() : Date.now()) - new Date(start).getTime()
  if (milliseconds < 1000) return `${Math.max(0, milliseconds)}ms`
  const seconds = Math.floor(milliseconds / 1000)
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`
}

function formatBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`
}

function shellToken(value: string) {
  return /^[a-zA-Z0-9_./:@%+=,-]+$/.test(value) ? value : JSON.stringify(value)
}
