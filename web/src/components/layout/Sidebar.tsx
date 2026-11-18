import type { ReactNode } from 'react';
import { Link } from '@tanstack/react-router';
import { UserMenu } from './UserMenu';
import { WorkspaceSwitcher } from './WorkspaceSwitcher';
import type { Me, Workspace } from '@/lib/api/types';
import { canManageAlertChannels } from '@/lib/roles';

export interface SidebarProps {
	me: Me;
	currentWorkspace?: Workspace;
	onNavigate?: () => void;
}

const linkClass =
	'flex h-9 items-center rounded-lg px-2 text-sm text-muted-foreground hover:bg-muted hover:text-foreground [&[aria-current=page]]:bg-muted [&[aria-current=page]]:font-medium [&[aria-current=page]]:text-foreground';
const activeClass = 'bg-muted';

function NavGroup({ title, children }: { title: string; children: ReactNode }) {
	return (
		<div className="space-y-1 border-t pt-3 first:border-t-0 first:pt-0">
			<h2 className="px-2 pb-1 text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
				{title}
			</h2>
			{children}
		</div>
	);
}

export function Sidebar({ me, currentWorkspace, onNavigate }: SidebarProps) {
	return (
		<nav aria-label="Main" className="flex flex-1 flex-col gap-3">
			<WorkspaceSwitcher
				workspaces={me.workspaces}
				current={currentWorkspace}
				onNavigate={onNavigate}
			/>

			{currentWorkspace ? (
				<NavGroup title="Workspace">
					<Link
						to="/w/$workspaceSlug/monitors"
						params={{ workspaceSlug: currentWorkspace.slug }}
						className={linkClass}
						activeProps={{ className: activeClass }}
						onClick={onNavigate}
					>
						Monitors
					</Link>
					<Link
						to="/w/$workspaceSlug/incidents"
						params={{ workspaceSlug: currentWorkspace.slug }}
						className={linkClass}
						activeProps={{ className: activeClass }}
						onClick={onNavigate}
					>
						Incidents
					</Link>
					<Link
						to="/w/$workspaceSlug/members"
						params={{ workspaceSlug: currentWorkspace.slug }}
						className={linkClass}
						activeProps={{ className: activeClass }}
						onClick={onNavigate}
					>
						Members
					</Link>
				</NavGroup>
			) : (
				<p className="px-2 text-xs text-muted-foreground">
					Select a workspace to manage monitors.
				</p>
			)}

			{currentWorkspace && canManageAlertChannels(currentWorkspace.role) ? (
				<NavGroup title="Settings">
					<Link
						to="/w/$workspaceSlug/settings/alerts"
						params={{ workspaceSlug: currentWorkspace.slug }}
						className={linkClass}
						activeProps={{ className: activeClass }}
						onClick={onNavigate}
					>
						Alerts
					</Link>
					<Link
						to="/w/$workspaceSlug/settings/status-page"
						params={{ workspaceSlug: currentWorkspace.slug }}
						className={linkClass}
						activeProps={{ className: activeClass }}
						onClick={onNavigate}
					>
						Status page
					</Link>
					<Link
						to="/w/$workspaceSlug/settings/billing"
						params={{ workspaceSlug: currentWorkspace.slug }}
						className={linkClass}
						activeProps={{ className: activeClass }}
						onClick={onNavigate}
					>
						Billing
					</Link>
					<Link
						to="/w/$workspaceSlug/settings/api-keys"
						params={{ workspaceSlug: currentWorkspace.slug }}
						className={linkClass}
						activeProps={{ className: activeClass }}
						onClick={onNavigate}
					>
						API keys
					</Link>
				</NavGroup>
			) : null}

			{me.user.is_admin ? (
				<NavGroup title="Admin">
					<Link
						to="/admin/workspaces"
						className={linkClass}
						activeProps={{ className: activeClass }}
						onClick={onNavigate}
					>
						Workspaces
					</Link>
					<Link
						to="/admin/monitors"
						className={linkClass}
						activeProps={{ className: activeClass }}
						onClick={onNavigate}
					>
						Monitors
					</Link>
					<Link
						to="/admin/security-events"
						className={linkClass}
						activeProps={{ className: activeClass }}
						onClick={onNavigate}
					>
						Security events
					</Link>
				</NavGroup>
			) : null}

			<div className="mt-auto border-t pt-4">
				<UserMenu email={me.user.email} />
			</div>
		</nav>
	);
}
