import { useState } from 'react';
import { Link } from '@tanstack/react-router';
import { EmptyState } from '@/components/feedback/EmptyState';
import { ErrorPanel } from '@/components/feedback/ErrorPanel';
import { PageSkeleton } from '@/components/feedback/Skeletons';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import {
	Table,
	TableBody,
	TableCaption,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from '@/components/ui/table';
import { isApiProblem } from '@/lib/api/client';
import { problemFromError } from '@/lib/api/problem-from-error';
import type { IncidentFilters, Workspace } from '@/lib/api/types';
import { formatRelativeTime } from '@/lib/format';
import { useIncidents } from './hooks';
import {
	formatIncidentDuration,
	incidentStatusLabel,
	incidentStatusVariant,
} from './status';

const STATUS_FILTERS: { value: IncidentFilters['status']; label: string }[] = [
	{ value: 'active', label: 'Active' },
	{ value: 'open', label: 'Open' },
	{ value: 'acknowledged', label: 'Acknowledged' },
	{ value: 'resolved', label: 'Resolved' },
	{ value: 'all', label: 'All' },
];

function errorMessage(error: unknown): string {
	if (isApiProblem(error)) {
		return error.problem.detail ?? error.problem.title;
	}
	if (error instanceof Error) return error.message;
	return 'Please try again.';
}

export interface IncidentsScreenProps {
	workspace: Workspace;
}

export function IncidentsScreen({ workspace }: IncidentsScreenProps) {
	const [status, setStatus] = useState<IncidentFilters['status']>('active');
	const incidentsQuery = useIncidents(workspace.id, { status });
	const incidents =
		incidentsQuery.data?.pages.flatMap((page) => page.incidents) ?? [];

	return (
		<section className="space-y-6">
			<div>
				<h1 className="font-heading text-xl font-semibold">Incidents</h1>
				<p className="text-sm text-muted-foreground">
					Monitor incidents for {workspace.name}.
				</p>
			</div>

			<div
				role="group"
				aria-label="Incident status"
				className="flex flex-wrap items-center gap-1 rounded-lg bg-muted p-1"
			>
				{STATUS_FILTERS.map((filter) => (
					<Button
						key={filter.value}
						type="button"
						size="sm"
						variant={filter.value === status ? 'secondary' : 'ghost'}
						aria-pressed={filter.value === status}
						onClick={() => setStatus(filter.value)}
					>
						{filter.label}
					</Button>
				))}
			</div>

			{incidentsQuery.isPending ? (
				<PageSkeleton />
			) : incidentsQuery.isError && incidents.length === 0 ? (
				<ErrorPanel
					message={errorMessage(incidentsQuery.error)}
					problem={problemFromError(incidentsQuery.error)}
					onRetry={() => void incidentsQuery.refetch()}
				/>
			) : incidents.length === 0 ? (
				<EmptyState title="No incidents" />
			) : (
				<>
					<Table>
						<TableCaption className="sr-only">
							Incidents for {workspace.name}
						</TableCaption>
						<TableHeader>
							<TableRow>
								<TableHead>Monitor</TableHead>
								<TableHead>Status</TableHead>
								<TableHead>Started</TableHead>
								<TableHead>Duration</TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{incidents.map((incident) => (
								<TableRow key={incident.id}>
									<TableCell className="font-medium">
										<Link
											to="/w/$workspaceSlug/incidents/$incidentId"
											params={{
												workspaceSlug: workspace.slug,
												incidentId: incident.id,
											}}
											aria-label={`View ${incident.monitor_name} incident`}
											className="underline underline-offset-4"
										>
											{incident.monitor_name}
										</Link>
									</TableCell>
									<TableCell>
										<Badge variant={incidentStatusVariant(incident.status)}>
											{incidentStatusLabel(incident.status)}
										</Badge>
									</TableCell>
									<TableCell>
										<span title={incident.started_at}>
											{formatRelativeTime(incident.started_at)}
										</span>
									</TableCell>
									<TableCell>
										{formatIncidentDuration(
											incident.started_at,
											incident.resolved_at,
										)}
									</TableCell>
								</TableRow>
							))}
						</TableBody>
					</Table>

					{incidentsQuery.hasNextPage ? (
						<div className="flex justify-center">
							<Button
								type="button"
								variant="outline"
								disabled={incidentsQuery.isFetchingNextPage}
								onClick={() => void incidentsQuery.fetchNextPage()}
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
