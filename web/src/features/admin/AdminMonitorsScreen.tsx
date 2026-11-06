import { useMemo, useState } from 'react';
import { EmptyState } from '@/components/feedback/EmptyState';
import { ErrorPanel } from '@/components/feedback/ErrorPanel';
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
import { problemFromError } from '@/lib/api/problem-from-error';
import type { AdminMonitorFilters } from '@/lib/api/types';
import { formatLatency, formatRelativeTime } from '@/lib/format';
import { statusLabel, statusVariant } from '@/features/monitors/status';
import { useAdminMonitors } from './hooks';
import { useDebouncedValue } from './useDebouncedValue';
import { WorkspaceCombobox } from './WorkspaceCombobox';

function errorMessage(error: unknown): string {
	if (isApiProblem(error)) {
		return error.problem.detail ?? error.problem.title;
	}
	if (error instanceof Error) return error.message;
	return 'Please try again.';
}

export function AdminMonitorsScreen() {
	const [search, setSearch] = useState('');
	const debouncedSearch = useDebouncedValue(search.trim(), 300);
	const [workspaceId, setWorkspaceId] = useState<string | null>(null);

	const filters: AdminMonitorFilters = {
		query: debouncedSearch,
		workspaceId: workspaceId ?? undefined,
	};
	const monitorsQuery = useAdminMonitors(filters);

	const monitors = useMemo(
		() => monitorsQuery.data?.pages.flatMap((page) => page.monitors) ?? [],
		[monitorsQuery.data],
	);

	return (
		<section className="space-y-6">
			<h2 className="font-heading text-lg font-semibold">Monitors</h2>

			<div className="flex flex-wrap items-end gap-3">
				<div className="min-w-56 flex-1 space-y-1">
					<Label htmlFor="admin-monitor-search">Search</Label>
					<Input
						id="admin-monitor-search"
						type="search"
						placeholder="Search monitors"
						value={search}
						onChange={(event) => setSearch(event.target.value)}
					/>
				</div>
				<div className="w-64">
					<WorkspaceCombobox value={workspaceId} onChange={setWorkspaceId} />
				</div>
			</div>

			{monitorsQuery.isPending ? (
				<PageSkeleton />
			) : monitorsQuery.isError && monitors.length === 0 ? (
				<ErrorPanel
					message={errorMessage(monitorsQuery.error)}
					problem={problemFromError(monitorsQuery.error)}
					onRetry={() => void monitorsQuery.refetch()}
				/>
			) : monitors.length === 0 ? (
				<EmptyState
					title="No monitors"
					description="No monitors match the current filters."
				/>
			) : (
				<>
					<div className="overflow-x-auto">
						<Table>
							<TableHeader>
								<TableRow>
									<TableHead>Workspace</TableHead>
									<TableHead>Name</TableHead>
									<TableHead>URL</TableHead>
									<TableHead>Status</TableHead>
									<TableHead>Last check</TableHead>
									<TableHead>Latency</TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{monitors.map((monitor) => (
									<TableRow key={monitor.id}>
										<TableCell>{monitor.workspace_name}</TableCell>
										<TableCell className="font-medium">
											{monitor.name}
										</TableCell>
										<TableCell className="max-w-96 truncate font-mono text-xs">
											{monitor.url}
										</TableCell>
										<TableCell>
											<Badge variant={statusVariant(monitor.status)}>
												{statusLabel(monitor.status)}
											</Badge>
										</TableCell>
										<TableCell>
											{monitor.last_checked_at
												? formatRelativeTime(monitor.last_checked_at)
												: 'Never checked'}
										</TableCell>
										<TableCell>
											{formatLatency(monitor.last_latency_ms)}
										</TableCell>
									</TableRow>
								))}
							</TableBody>
						</Table>
					</div>

					{monitorsQuery.hasNextPage ? (
						<div className="flex justify-center">
							<Button
								type="button"
								variant="outline"
								disabled={monitorsQuery.isFetchingNextPage}
								onClick={() => void monitorsQuery.fetchNextPage()}
							>
								Load more
							</Button>
						</div>
					) : null}
				</>
			)}
		</section>
	);
}
