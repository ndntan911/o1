import { Suspense, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link, Outlet, useParams } from '@tanstack/react-router';
import { MenuIcon } from 'lucide-react';
import { PageSkeleton } from '@/components/feedback/Skeletons';
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert';
import { Button } from '@/components/ui/button';
import {
	Sheet,
	SheetContent,
	SheetHeader,
	SheetTitle,
	SheetTrigger,
} from '@/components/ui/sheet';
import { findWorkspace } from '@/features/auth/guards';
import { useWorkspaceSuspended } from '@/lib/workspace-status';
import { meQueryOptions } from '@/lib/query/me';
import { Sidebar } from './Sidebar';

export function AppShell() {
	const me = useQuery(meQueryOptions());
	const params = useParams({ strict: false }) as {
		workspaceSlug?: string;
	};
	const suspended = useWorkspaceSuspended();
	const [mobileOpen, setMobileOpen] = useState(false);

	const user = me.data;
	const currentWorkspace =
		user && params.workspaceSlug
			? findWorkspace(user, params.workspaceSlug)
			: undefined;

	if (!user) {
		return (
			<div className="min-h-screen bg-background p-6">
				<PageSkeleton />
			</div>
		);
	}

	const sidebar = (
		<Sidebar
			me={user}
			currentWorkspace={currentWorkspace}
			onNavigate={() => setMobileOpen(false)}
		/>
	);

	return (
		<div className="min-h-screen bg-background">
			<a
				href="#main-content"
				className="sr-only focus:not-sr-only focus:absolute focus:top-4 focus:left-4 focus:z-50 focus:rounded-lg focus:bg-background focus:px-3 focus:py-2 focus:ring-1 focus:ring-ring"
			>
				Skip to content
			</a>

			<aside className="fixed inset-y-0 left-0 hidden w-64 flex-col border-r bg-background p-4 md:flex">
				<Link to="/" className="mb-4 px-2 font-heading text-lg font-medium">
					o1 uptime
				</Link>
				{sidebar}
			</aside>

			<div className="md:pl-64">
				<header className="flex items-center gap-3 border-b px-4 py-2 md:hidden">
					<Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
						<SheetTrigger asChild>
							<Button
								type="button"
								variant="ghost"
								size="icon"
								aria-label="Open navigation"
							>
								<MenuIcon />
							</Button>
						</SheetTrigger>
						<SheetContent
							side="left"
							className="flex w-72 max-w-[85vw] flex-col gap-4 p-4"
						>
							<SheetHeader className="p-0">
								<SheetTitle>Navigation</SheetTitle>
							</SheetHeader>
							{sidebar}
						</SheetContent>
					</Sheet>
					<Link to="/" className="font-heading text-base font-medium">
						o1 uptime
					</Link>
				</header>

				{suspended ? (
					<Alert
						variant="destructive"
						className="rounded-none border-x-0 border-t-0"
					>
						<AlertTitle>Workspace suspended</AlertTitle>
						<AlertDescription>
							Changes are unavailable while this workspace is suspended. You can
							still view its monitors.
						</AlertDescription>
					</Alert>
				) : null}

				<main id="main-content" className="p-4 md:p-8">
					<Suspense fallback={<PageSkeleton />}>
						<Outlet />
					</Suspense>
				</main>
			</div>
		</div>
	);
}
