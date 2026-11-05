import { useMemo, useState } from 'react';
import { ConfirmDialog } from '@/components/feedback/ConfirmDialog';
import { EmptyState } from '@/components/feedback/EmptyState';
import { ErrorPanel } from '@/components/feedback/ErrorPanel';
import { ProblemAlert } from '@/components/feedback/ProblemAlert';
import { PageSkeleton } from '@/components/feedback/Skeletons';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from '@/components/ui/table';
import { isApiProblem } from '@/lib/api/client';
import type { ApiProblem } from '@/lib/api/problems';
import type { AdminWorkspace, AdminWorkspaceFilters } from '@/lib/api/types';
import { useAdminWorkspaces, useUnsuspendWorkspace } from './hooks';
import { SuspendDialog } from './SuspendDialog';
import { useDebouncedValue } from './useDebouncedValue';

type StatusFilter = 'all' | 'active' | 'suspended';

const dateFormatter = new Intl.DateTimeFormat('en', { dateStyle: 'medium' });

function formatDate(iso: string): string {
	return dateFormatter.format(new Date(iso));
}

function errorMessage(error: unknown): string {
	if (isApiProblem(error)) {
		return error.problem.detail ?? error.problem.title;
	}
	if (error instanceof Error) return error.message;
	return 'Please try again.';
}

function unexpectedProblem(error: unknown): ApiProblem {
	if (isApiProblem(error)) return error.problem;
	return {
		type: 'about:blank',
		title: 'Something went wrong',
		status: 0,
		code: 'internal_error',
		detail: 'Please try again.',
	};
}

export function AdminWorkspacesScreen() {
	const [search, setSearch] = useState('');
	const debouncedSearch = useDebouncedValue(search.trim(), 300);
	const [status, setStatus] = useState<StatusFilter>('all');
	const [suspendTarget, setSuspendTarget] = useState<AdminWorkspace | null>(
		null,
	);
	const [unsuspendTarget, setUnsuspendTarget] = useState<AdminWorkspace | null>(
		null,
	);
	const [problem, setProblem] = useState<ApiProblem | null>(null);

	const filters: AdminWorkspaceFilters = {
		query: debouncedSearch,
		suspended:
			status === 'all' ? undefined : status === 'suspended' ? true : false,
	};
	const workspacesQuery = useAdminWorkspaces(filters);
	const unsuspend = useUnsuspendWorkspace();

	const workspaces = useMemo(
		() => workspacesQuery.data?.pages.flatMap((page) => page.workspaces) ?? [],
		[workspacesQuery.data],
	);

	function handleProblem(error: unknown) {
		setProblem(unexpectedProblem(error));
	}

	return (
		<section className="space-y-6">
			<h2 className="font-heading text-lg font-semibold">Workspaces</h2>

			<div className="flex flex-wrap items-end gap-3">
				<div className="min-w-56 flex-1 space-y-1">
					<Label htmlFor="admin-workspace-search">Search</Label>
					<Input
						id="admin-workspace-search"
						type="search"
						placeholder="Search workspaces"
						value={search}
						onChange={(event) => setSearch(event.target.value)}
					/>
				</div>
				<div className="space-y-1">
					<Label htmlFor="admin-workspace-status">Status</Label>
					<select
						id="admin-workspace-status"
						className="h-8 rounded-lg border border-input bg-transparent px-2 text-sm"
						value={status}
						onChange={(event) => setStatus(event.target.value as StatusFilter)}
					>
						<option value="all">All</option>
						<option value="active">Active</option>
						<option value="suspended">Suspended</option>
					</select>
				</div>
			</div>

			{problem ? <ProblemAlert problem={problem} /> : null}

			{workspacesQuery.isPending ? (
				<PageSkeleton />
			) : workspacesQuery.isError && workspaces.length === 0 ? (
				<ErrorPanel
					message={errorMessage(workspacesQuery.error)}
					onRetry={() => void workspacesQuery.refetch()}
				/>
			) : workspaces.length === 0 ? (
				<EmptyState
					title="No workspaces"
					description="No workspaces match the current filters."
				/>
			) : (
				<>
					<div className="overflow-x-auto">
						<Table>
							<TableHeader>
								<TableRow>
									<TableHead>Name</TableHead>
									<TableHead>Slug</TableHead>
									<TableHead>Created</TableHead>
									<TableHead>Members</TableHead>
									<TableHead>Monitors</TableHead>
									<TableHead>Status</TableHead>
									<TableHead>Reason</TableHead>
									<TableHead className="text-right">Actions</TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{workspaces.map((workspace) => {
									const suspended = workspace.suspended_at !== null;
									return (
										<TableRow key={workspace.id}>
											<TableCell className="font-medium">
												{workspace.name}
											</TableCell>
											<TableCell className="font-mono text-xs">
												{workspace.slug}
											</TableCell>
											<TableCell>{formatDate(workspace.created_at)}</TableCell>
											<TableCell>{workspace.member_count}</TableCell>
											<TableCell>{workspace.monitor_count}</TableCell>
											<TableCell>
												<Badge variant={suspended ? 'destructive' : 'outline'}>
													{suspended ? 'Suspended' : 'Active'}
												</Badge>
											</TableCell>
											<TableCell className="max-w-64 truncate text-sm text-muted-foreground">
												{workspace.suspended_reason ?? '—'}
											</TableCell>
											<TableCell className="text-right">
												{suspended ? (
													<Button
														type="button"
														variant="outline"
														size="sm"
														onClick={() => setUnsuspendTarget(workspace)}
													>
														Unsuspend
													</Button>
												) : (
													<Button
														type="button"
														variant="destructive"
														size="sm"
														onClick={() => setSuspendTarget(workspace)}
													>
														Suspend
													</Button>
												)}
											</TableCell>
										</TableRow>
									);
								})}
							</TableBody>
						</Table>
					</div>

					{workspacesQuery.hasNextPage ? (
						<div className="flex justify-center">
							<Button
								type="button"
								variant="outline"
								disabled={workspacesQuery.isFetchingNextPage}
								onClick={() => void workspacesQuery.fetchNextPage()}
							>
								Load more
							</Button>
						</div>
					) : null}
				</>
			)}

			{suspendTarget ? (
				<SuspendDialog
					workspace={suspendTarget}
					onClose={() => setSuspendTarget(null)}
				/>
			) : null}

			<ConfirmDialog
				open={unsuspendTarget !== null}
				title={
					unsuspendTarget
						? `Unsuspend ${unsuspendTarget.name}?`
						: 'Unsuspend workspace'
				}
				description="Members will be able to change monitors again."
				confirmLabel="Unsuspend workspace"
				pending={unsuspend.isPending}
				onConfirm={() => {
					if (!unsuspendTarget) return;
					unsuspend.mutate(unsuspendTarget.id, {
						onSuccess: () => setUnsuspendTarget(null),
						onError: handleProblem,
					});
				}}
				onOpenChange={(open) => {
					if (!open) setUnsuspendTarget(null);
				}}
			/>
		</section>
	);
}
