import { useState } from 'react';
import { Link } from '@tanstack/react-router';
import { EmptyState } from '@/components/feedback/EmptyState';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import {
	Card,
	CardDescription,
	CardFooter,
	CardHeader,
	CardTitle,
} from '@/components/ui/card';
import type { Workspace } from '@/lib/api/types';
import { WorkspaceCreateDialog } from './WorkspaceCreateDialog';

export interface WorkspaceListScreenProps {
	workspaces: Workspace[];
}

export function WorkspaceListScreen({ workspaces }: WorkspaceListScreenProps) {
	const [createOpen, setCreateOpen] = useState(false);

	return (
		<section className="space-y-6">
			<div className="flex flex-wrap items-center justify-between gap-3">
				<div>
					<h1 className="font-heading text-xl font-semibold">Workspaces</h1>
					<p className="text-sm text-muted-foreground">
						Choose a workspace to manage its monitors and members.
					</p>
				</div>
				{workspaces.length > 0 ? (
					<Button type="button" onClick={() => setCreateOpen(true)}>
						New workspace
					</Button>
				) : null}
			</div>

			{workspaces.length === 0 ? (
				<EmptyState
					title="No workspaces yet"
					description="Create a workspace to start adding uptime monitors."
					action={
						<Button type="button" onClick={() => setCreateOpen(true)}>
							New workspace
						</Button>
					}
				/>
			) : (
				<div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
					{workspaces.map((workspace) => (
						<Card key={workspace.id}>
							<CardHeader>
								<CardTitle>{workspace.name}</CardTitle>
								<CardDescription className="flex items-center gap-2">
									<Badge variant="outline">{workspace.role}</Badge>
									<span className="truncate">{workspace.slug}</span>
								</CardDescription>
							</CardHeader>
							<CardFooter>
								<Link
									to="/w/$workspaceSlug/monitors"
									params={{ workspaceSlug: workspace.slug }}
									className="text-sm font-medium text-primary underline-offset-4 hover:underline"
								>
									View monitors
								</Link>
							</CardFooter>
						</Card>
					))}
				</div>
			)}

			<WorkspaceCreateDialog open={createOpen} onOpenChange={setCreateOpen} />
		</section>
	);
}
