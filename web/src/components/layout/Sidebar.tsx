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
	'flex h-9 items-center rounded-lg px-2 text-sm text-muted-foreground hover:bg-muted hover:text-foreground [&[aria-current=page]]:text-foreground';
const activeClass = 'bg-muted';

export function Sidebar({ me, currentWorkspace, onNavigate }: SidebarProps) {
	return (
		<nav aria-label="Main" className="flex flex-1 flex-col gap-4">
			<WorkspaceSwitcher
				workspaces={me.workspaces}
				current={currentWorkspace}
				onNavigate={onNavigate}
			/>

			<div className="space-y-1">
				{currentWorkspace ? (
					<>
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
					</>
				) : (
					<p className="px-2 text-xs text-muted-foreground">
						Select a workspace to manage monitors.
					</p>
				)}
			</div>

			{currentWorkspace && canManageAlertChannels(currentWorkspace.role) ? (
				<div className="space-y-1">
					<h2 className="px-2 text-xs font-medium text-muted-foreground">
						Settings
					</h2>
					<Link
						to="/w/$workspaceSlug/settings/alerts"
						params={{ workspaceSlug: currentWorkspace.slug }}
						className={linkClass}
						activeProps={{ className: activeClass }}
						onClick={onNavigate}
					>
						Alerts
					</Link>
				</div>
			) : null}

			{me.user.is_admin ? (
				<div className="space-y-1">
					<h2 className="px-2 text-xs font-medium text-muted-foreground">
						Admin
					</h2>
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
				</div>
			) : null}

			<div className="mt-auto border-t pt-4">
				<UserMenu email={me.user.email} />
			</div>
		</nav>
	);
}
