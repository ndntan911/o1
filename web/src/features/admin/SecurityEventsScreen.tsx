import { useMemo, useState } from 'react';
import { EmptyState } from '@/components/feedback/EmptyState';
import { ErrorPanel } from '@/components/feedback/ErrorPanel';
import { PageSkeleton } from '@/components/feedback/Skeletons';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Label } from '@/components/ui/label';
import { isApiProblem } from '@/lib/api/client';
import { problemFromError } from '@/lib/api/problem-from-error';
import type { SecurityEventFilters } from '@/lib/api/types';
import { formatRelativeTime } from '@/lib/format';
import { SECURITY_EVENT_KINDS } from './api';
import { EventDetail } from './EventDetail';
import { useSecurityEvents } from './hooks';
import { WorkspaceCombobox } from './WorkspaceCombobox';

function errorMessage(error: unknown): string {
	if (isApiProblem(error)) {
		return error.problem.detail ?? error.problem.title;
	}
	if (error instanceof Error) return error.message;
	return 'Please try again.';
}

export function SecurityEventsScreen() {
	const [kind, setKind] = useState('');
	const [workspaceId, setWorkspaceId] = useState<string | null>(null);

	const filters: SecurityEventFilters = {
		kind: kind || undefined,
		workspaceId: workspaceId ?? undefined,
	};
	const eventsQuery = useSecurityEvents(filters);

	const events = useMemo(() => {
		const loaded = eventsQuery.data?.pages.flatMap((page) => page.events) ?? [];
		return [...loaded].sort(
			(a, b) =>
				new Date(b.created_at).getTime() - new Date(a.created_at).getTime(),
		);
	}, [eventsQuery.data]);

	return (
		<section className="space-y-6">
			<h2 className="font-heading text-lg font-semibold">Security events</h2>

			<div className="flex flex-wrap items-end gap-3">
				<div className="space-y-1">
					<Label htmlFor="security-event-kind">Kind</Label>
					<select
						id="security-event-kind"
						className="h-8 rounded-lg border border-input bg-transparent px-2 text-sm"
						value={kind}
						onChange={(event) => setKind(event.target.value)}
					>
						<option value="">All kinds</option>
						{SECURITY_EVENT_KINDS.map((value) => (
							<option key={value} value={value}>
								{value}
							</option>
						))}
					</select>
				</div>
				<div className="w-64">
					<WorkspaceCombobox value={workspaceId} onChange={setWorkspaceId} />
				</div>
			</div>

			{eventsQuery.isPending ? (
				<PageSkeleton />
			) : eventsQuery.isError && events.length === 0 ? (
				<ErrorPanel
					message={errorMessage(eventsQuery.error)}
					problem={problemFromError(eventsQuery.error)}
					onRetry={() => void eventsQuery.refetch()}
				/>
			) : events.length === 0 ? (
				<EmptyState
					title="No security events"
					description="No security events match the current filters."
				/>
			) : (
				<>
					<ul aria-label="Security events" className="space-y-3">
						{events.map((event) => (
							<li key={event.id} className="space-y-2 rounded-xl border p-4">
								<div className="flex flex-wrap items-center justify-between gap-2">
									<div className="flex flex-wrap items-center gap-2">
										<Badge variant="outline">{event.kind}</Badge>
										<span className="text-sm text-muted-foreground">
											{formatRelativeTime(event.created_at)}
										</span>
									</div>
									<span className="text-sm">
										{event.workspace_id ?? 'No workspace'}
									</span>
								</div>
								<EventDetail detail={event.detail} />
							</li>
						))}
					</ul>

					{eventsQuery.hasNextPage ? (
						<div className="flex justify-center">
							<Button
								type="button"
								variant="outline"
								disabled={eventsQuery.isFetchingNextPage}
								onClick={() => void eventsQuery.fetchNextPage()}
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
