import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { toast } from 'sonner'
import { Empty, Flash, PageHeader, Panel } from '@/components/page'
import { Pill } from '@/components/status'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { api, errorMessage } from '@/lib/api'
import { dateTime } from '@/lib/format'
import type { User, UserRole } from '@/lib/types'

const roles: { id: UserRole; label: string; hint: string }[] = [
  { id: 'viewer', label: 'Viewer', hint: 'Read dashboards and reports' },
  { id: 'operator', label: 'Operator', hint: 'Viewer + back up now, list snapshots, restore' },
  { id: 'admin', label: 'Admin', hint: 'Operator + servers, configuration, users, audit log' },
]

function RoleSelect({ value, onChange, label }: { value: UserRole; onChange: (r: UserRole) => void; label: string }) {
  return (
    <Select aria-label={label} selectedKey={value} onSelectionChange={(k) => onChange(String(k) as UserRole)}>
      <SelectTrigger>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {roles.map((r) => (
          <SelectItem key={r.id} id={r.id} textValue={r.label}>
            {r.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

export function UsersPage() {
  const qc = useQueryClient()
  const users = useQuery({ queryKey: ['users'], queryFn: () => api<User[]>('GET', '/api/ui/users') })

  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState<UserRole>('viewer')
  const [passwordFor, setPasswordFor] = useState<User | null>(null)
  const [newPassword, setNewPassword] = useState('')

  const refresh = () => qc.invalidateQueries({ queryKey: ['users'] })

  const create = useMutation({
    mutationFn: () => api<User>('POST', '/api/ui/users', { username, password, role }),
    onSuccess: (u) => {
      toast.success(`Created ${u.username}`)
      setUsername('')
      setPassword('')
      refresh()
    },
    onError: (e) => toast.error(errorMessage(e)),
  })

  const update = useMutation({
    mutationFn: (v: { user: User; patch: { role?: UserRole; disabled?: boolean; password?: string } }) =>
      api<User>('PATCH', `/api/ui/users/${v.user.id}`, v.patch),
    onSuccess: (u, v) => {
      toast.success(v.patch.password !== undefined ? `Password changed for ${u.username}; they were signed out` : `Updated ${u.username}`)
      if (v.patch.password !== undefined) {
        setPasswordFor(null)
        setNewPassword('')
      }
      refresh()
    },
    onError: (e) => toast.error(errorMessage(e)),
  })

  return (
    <>
      <PageHeader title="Users" subtitle="Who can sign in, and what they may do. Every action is recorded in the audit log." />

      <Panel title="Add user" className="mb-6">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            create.mutate()
          }}
          className="grid items-end gap-3 md:grid-cols-[1fr_1fr_12rem_auto]"
        >
          <div className="grid gap-1.5">
            <Label htmlFor="new-username">Username</Label>
            <Input id="new-username" required autoComplete="off" pattern="[a-zA-Z0-9][a-zA-Z0-9._\-]{2,31}" value={username} onChange={(e) => setUsername(e.target.value)} />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="new-password">Password</Label>
            <Input id="new-password" type="password" required minLength={10} maxLength={72} autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />
          </div>
          <div className="grid gap-1.5">
            <Label>Role</Label>
            <RoleSelect label="Role" value={role} onChange={setRole} />
          </div>
          <Button type="submit" isDisabled={create.isPending}>
            Add user
          </Button>
        </form>
        <p className="mt-3 text-xs text-muted-foreground">
          {roles.find((r) => r.id === role)?.hint}. Passwords need at least 10 characters; share them over a safe channel.
        </p>
      </Panel>

      {passwordFor && (
        <Panel title={`Set a new password for ${passwordFor.username}`} className="mb-6">
          <form
            onSubmit={(e) => {
              e.preventDefault()
              update.mutate({ user: passwordFor, patch: { password: newPassword } })
            }}
            className="flex flex-wrap items-end gap-3"
          >
            <div className="grid min-w-64 flex-1 gap-1.5">
              <Label htmlFor="reset-password">New password</Label>
              <Input id="reset-password" type="password" required minLength={10} maxLength={72} autoFocus autoComplete="new-password" value={newPassword} onChange={(e) => setNewPassword(e.target.value)} />
            </div>
            <Button type="submit" isDisabled={update.isPending}>
              Change password
            </Button>
            <Button variant="outline" onPress={() => setPasswordFor(null)}>
              Cancel
            </Button>
          </form>
        </Panel>
      )}

      <Panel title="Accounts">
        {users.error && <Flash tone="bad">{errorMessage(users.error)}</Flash>}
        {users.data && users.data.length === 0 && <Empty>No users yet.</Empty>}
        {users.data && users.data.length > 0 && (
          <Table aria-label="Users">
            <TableHeader>
              <TableHead isRowHeader>Username</TableHead>
              <TableHead>Role</TableHead>
              <TableHead>Status</TableHead>
              <TableHead className="hidden md:table-cell">Created</TableHead>
              <TableHead aria-label="Actions" />
            </TableHeader>
            <TableBody>
              {users.data.map((u) => (
                <TableRow key={u.username}>
                  <TableCell className="font-semibold">
                    {u.username}
                    {u.bootstrap && <span className="ml-2 text-xs font-normal text-muted-foreground">from server.yaml</span>}
                  </TableCell>
                  <TableCell>
                    {u.bootstrap ? (
                      <Pill tone="idle">admin</Pill>
                    ) : (
                      <RoleSelect label={`Role of ${u.username}`} value={u.role} onChange={(r) => update.mutate({ user: u, patch: { role: r } })} />
                    )}
                  </TableCell>
                  <TableCell>{u.disabled ? <Pill tone="bad">disabled</Pill> : <Pill tone="ok">active</Pill>}</TableCell>
                  <TableCell className="hidden text-muted-foreground md:table-cell">{u.created_at ? dateTime(u.created_at) : '—'}</TableCell>
                  <TableCell>
                    {!u.bootstrap && (
                      <div className="flex justify-end gap-2">
                        <Button variant="outline" size="sm" onPress={() => setPasswordFor(u)}>
                          Set password
                        </Button>
                        <Button variant="outline" size="sm" onPress={() => update.mutate({ user: u, patch: { disabled: !u.disabled } })}>
                          {u.disabled ? 'Enable' : 'Disable'}
                        </Button>
                      </div>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
        <p className="mt-3 text-xs text-muted-foreground">
          The account from <code>server.yaml</code> always works and cannot be changed here. Disabling a user or changing their password signs them out.
        </p>
      </Panel>
    </>
  )
}
