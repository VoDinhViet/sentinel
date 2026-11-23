import { useInfiniteQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { Empty, Flash, PageHeader, Panel } from '@/components/page'
import { Pill, type Tone } from '@/components/status'
import { Button, buttonVariants } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { api, errorMessage } from '@/lib/api'
import { dateTime } from '@/lib/format'
import type { AuditEntry } from '@/lib/types'
import { cn } from 'cn'

const PAGE = 50

const actionFilters = [
  { id: '', label: 'All actions' },
  { id: 'auth.', label: 'Sign-ins' },
  { id: 'backup.', label: 'Backup and restore' },
  { id: 'config.', label: 'Configuration' },
  { id: 'dbjob.', label: 'Database jobs' },
  { id: 'agent.', label: 'Servers' },
  { id: 'user.', label: 'Users' },
  { id: 'beszel.', label: 'Monitoring' },
]

const outcomeFilters = [
  { id: '', label: 'Any outcome' },
  { id: 'ok', label: 'Succeeded' },
  { id: 'failed', label: 'Failed' },
  { id: 'denied', label: 'Denied' },
]

const outcomeTone: Record<AuditEntry['outcome'], Tone> = { ok: 'ok', failed: 'bad', denied: 'warn' }

function Filter({ label, value, onChange, options }: { label: string; value: string; onChange: (v: string) => void; options: { id: string; label: string }[] }) {
  return (
    <Select aria-label={label} selectedKey={value} onSelectionChange={(k) => onChange(String(k))}>
      <SelectTrigger>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {options.map((o) => (
          <SelectItem key={o.id || 'all'} id={o.id} textValue={o.label}>
            {o.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

// Details are small flat objects (job, version, role...); never secrets.
const detailText = (d: Record<string, unknown>) =>
  Object.entries(d)
    .map(([k, v]) => `${k}: ${String(v)}`)
    .join(' · ')

export function AuditPage() {
  const [user, setUser] = useState('')
  const [action, setAction] = useState('')
  const [outcome, setOutcome] = useState('')

  const params = new URLSearchParams()
  if (user.trim()) params.set('user', user.trim())
  if (action) params.set('action', action)
  if (outcome) params.set('outcome', outcome)

  const log = useInfiniteQuery({
    queryKey: ['audit', user.trim(), action, outcome],
    initialPageParam: 0,
    queryFn: ({ pageParam }) => {
      const q = new URLSearchParams(params)
      q.set('limit', String(PAGE))
      if (pageParam) q.set('before', String(pageParam))
      return api<AuditEntry[]>('GET', `/api/ui/audit?${q}`)
    },
    getNextPageParam: (last) => (last.length === PAGE ? last[last.length - 1].id : undefined),
    refetchInterval: 30_000,
  })
  const entries = log.data?.pages.flat() ?? []

  const csv = new URLSearchParams(params)
  csv.set('format', 'csv')

  return (
    <>
      <PageHeader
        title="Audit log"
        subtitle="Sign-ins, backups, restores and configuration changes. Entries can't be edited or deleted."
        actions={
          <a href={`/api/ui/audit?${csv}`} className={cn(buttonVariants({ variant: 'outline' }))}>
            Export CSV
          </a>
        }
      />
      <Panel
        title="Activity"
        actions={
          <>
            <div className="flex items-center gap-2">
              <Label htmlFor="audit-user" className="sr-only">
                User
              </Label>
              <Input id="audit-user" placeholder="Filter by user" className="w-40" value={user} onChange={(e) => setUser(e.target.value)} />
            </div>
            <Filter label="Action" value={action} onChange={setAction} options={actionFilters} />
            <Filter label="Outcome" value={outcome} onChange={setOutcome} options={outcomeFilters} />
          </>
        }
      >
        {log.error && <Flash tone="bad">{errorMessage(log.error)}</Flash>}
        {log.data && entries.length === 0 && <Empty>Nothing recorded for this filter.</Empty>}
        {entries.length > 0 && (
          <Table aria-label="Audit log">
            <TableHeader>
              <TableHead isRowHeader>Time</TableHead>
              <TableHead>User</TableHead>
              <TableHead>Action</TableHead>
              <TableHead className="hidden md:table-cell">Server / job</TableHead>
              <TableHead className="hidden md:table-cell">Outcome</TableHead>
              <TableHead className="hidden lg:table-cell">Details</TableHead>
              <TableHead className="hidden lg:table-cell">IP</TableHead>
            </TableHeader>
            <TableBody>
              {entries.map((e) => (
                <TableRow key={e.id}>
                  <TableCell className="whitespace-nowrap text-muted-foreground">{dateTime(e.at)}</TableCell>
                  <TableCell className="font-semibold">
                    {e.username || '—'}
                    <div className="mt-1 md:hidden">
                      <Pill tone={outcomeTone[e.outcome] ?? 'idle'}>{e.outcome}</Pill>
                    </div>
                  </TableCell>
                  <TableCell className="font-mono text-xs">{e.action}</TableCell>
                  <TableCell className="hidden md:table-cell">
                    {e.agent_name || e.job ? [e.agent_name, e.job].filter(Boolean).join(' / ') : '—'}
                  </TableCell>
                  <TableCell className="hidden md:table-cell">
                    <Pill tone={outcomeTone[e.outcome] ?? 'idle'}>{e.outcome}</Pill>
                  </TableCell>
                  <TableCell className="hidden max-w-80 truncate text-xs text-muted-foreground lg:table-cell">
                    <span title={detailText(e.details)}>{detailText(e.details) || '—'}</span>
                  </TableCell>
                  <TableCell className="hidden font-mono text-xs text-muted-foreground lg:table-cell">{e.ip || '—'}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
        {log.hasNextPage && (
          <div className="mt-4 flex justify-center">
            <Button variant="outline" isDisabled={log.isFetchingNextPage} onPress={() => log.fetchNextPage()}>
              Load older entries
            </Button>
          </div>
        )}
      </Panel>
    </>
  )
}
