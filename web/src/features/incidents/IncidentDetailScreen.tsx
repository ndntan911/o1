import { useState } from 'react';
import { Link } from '@tanstack/react-router';
import { ErrorPanel } from '@/components/feedback/ErrorPanel';
import { ProblemAlert } from '@/components/feedback/ProblemAlert';
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
import type { ApiProblem } from '@/lib/api/problems';
import type {
	IncidentDelivery,
	IncidentDetail,
	Workspace,
} from '@/lib/api/types';
import { formatRelativeTime } from '@/lib/format';
import { canAcknowledgeIncidents, canManageAlertChannels } from '@/lib/roles';
import {
	useAcknowledgeIncident,
	useIncident,
	useIncidentDeliveries,
} from './hooks';
import { IncidentTimeline } from './IncidentTimeline';
import { incidentStatusLabel, incidentStatusVariant } from './status';

const DELIVERY_EVENT_LABELS: Record<IncidentDelivery['event'], string> = {
	opened: 'Opened',
	reminder: 'Reminder',
	resolved: 'Resolved',
};

const DELIVERY_STATUS_LABELS: Record<IncidentDelivery['status'], string> = {
	pending: 'Pending',
	sent: 'Sent',
	failed: 'Failed',
	skipped: 'Skipped',
};

function errorMessage(error: unknown): string {
	if (isApiProblem(error)) {
		return error.problem.detail ?? error.problem.title;
	}
	if (error instanceof Error) return error.message;
	return 'Please try again.';
}

function isRetryable(error: unknown): boolean {
	if (!isApiProblem(error)) return true;
	return error.problem.status === 0 || error.problem.status >= 500;
}

export interface IncidentDetailScreenProps {
	workspace: Workspace;
	incidentId: string;
}

export function IncidentDetailScreen({
	workspace,
	incidentId,
}: IncidentDetailScreenProps) {
	const incidentQuery = useIncident(workspace.id, incidentId);

	if (incidentQuery.isPending) return <PageSkeleton />;

	if (!incidentQuery.data) {
		const notFound =
			isApiProblem(incidentQuery.error) &&
			incidentQuery.error.problem.code === 'not_found';
		return (
			<ErrorPanel
				title={notFound ? 'Incident not found' : 'Could not load incident'}
				message={
					notFound
						? 'This incident may have been removed.'
						: errorMessage(incidentQuery.error)
				}
				problem={problemFromError(incidentQuery.error)}
				onRetry={
					!notFound && isRetryable(incidentQuery.error)
						? () => void incidentQuery.refetch()
						: undefined
				}
			/>
		);
	}

	return (
		<IncidentDetailView
			key={incidentId}
			workspace={workspace}
			detail={incidentQuery.data}
		/>
	);
}

interface IncidentDetailViewProps {
	workspace: Workspace;
	detail: IncidentDetail;
}

function IncidentDetailView({ workspace, detail }: IncidentDetailViewProps) {
	const { incident, events } = detail;
	const acknowledge = useAcknowledgeIncident(workspace.id, incident.id);
	const deliveriesQuery = useIncidentDeliveries(
		workspace.id,
		incident.id,
		workspace.role,
	);
	const [problem, setProblem] = useState<ApiProblem | null>(null);
	const canAcknowledge =
		canAcknowledgeIncidents(workspace.role) && incident.status === 'open';
	const isAdmin = canManageAlertChannels(workspace.role);

	function runAcknowledge() {
		setProblem(null);
		acknowledge.mutate(undefined, {
			onError: (error) => setProblem(problemFromError(error)),
			onSuccess: () => setProblem(null),
		});
	}

	return (
		<section className="space-y-6">
			<Link
				to="/w/$workspaceSlug/incidents"
				params={{ workspaceSlug: workspace.slug }}
				className="text-sm text-muted-foreground underline underline-offset-4"
			>
				Back to incidents
			</Link>

			<div className="flex flex-wrap items-start justify-between gap-4">
				<div className="space-y-1">
					<div className="flex flex-wrap items-center gap-2">
						<h1 className="font-heading text-xl font-semibold">
							Incident: {incident.monitor_name}
						</h1>
						<Badge variant={incidentStatusVariant(incident.status)}>
							{incidentStatusLabel(incident.status)}
						</Badge>
					</div>
					<p className="text-sm text-muted-foreground">
						Started <span>{formatRelativeTime(incident.started_at)}</span>
						{incident.acknowledged_at ? (
							<span className="ml-2">
								· Acknowledged {formatRelativeTime(incident.acknowledged_at)}
							</span>
						) : null}
						{incident.resolved_at ? (
							<span className="ml-2">
								· Resolved {formatRelativeTime(incident.resolved_at)}
							</span>
						) : null}
					</p>
					<Link
						to="/w/$workspaceSlug/monitors/$monitorId"
						params={{
							workspaceSlug: workspace.slug,
							monitorId: incident.monitor_id,
						}}
						className="text-sm underline underline-offset-4"
					>
						View monitor
					</Link>
				</div>

				{canAcknowledge ? (
					<Button
						type="button"
						disabled={acknowledge.isPending}
						onClick={runAcknowledge}
					>
						Acknowledge
					</Button>
				) : null}
			</div>

			{problem ? <ProblemAlert problem={problem} /> : null}

			<div className="space-y-3">
				<h2 className="font-heading text-lg font-medium">Timeline</h2>
				<IncidentTimeline events={events} />
			</div>

			{isAdmin ? (
				<div className="space-y-3">
					<h2 className="font-heading text-lg font-medium">Alert deliveries</h2>
					{deliveriesQuery.isError && !deliveriesQuery.data ? (
						<ErrorPanel
							title="Could not load deliveries"
							message={errorMessage(deliveriesQuery.error)}
							problem={problemFromError(deliveriesQuery.error)}
							onRetry={
								isRetryable(deliveriesQuery.error)
									? () => void deliveriesQuery.refetch()
									: undefined
							}
						/>
					) : deliveriesQuery.isPending ? (
						<p className="text-sm text-muted-foreground" role="status">
							Loading deliveries…
						</p>
					) : deliveriesQuery.data.length === 0 ? (
						<p className="text-sm text-muted-foreground">No deliveries</p>
					) : (
						<Table>
							<TableCaption className="sr-only">
								Alert deliveries for this incident
							</TableCaption>
							<TableHeader>
								<TableRow>
									<TableHead>Event</TableHead>
									<TableHead>Recipient</TableHead>
									<TableHead>Status</TableHead>
									<TableHead>Attempts</TableHead>
									<TableHead>Sent</TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{deliveriesQuery.data.map((delivery) => (
									<TableRow key={delivery.id}>
										<TableCell>
											{DELIVERY_EVENT_LABELS[delivery.event]}
										</TableCell>
										<TableCell>{delivery.recipient}</TableCell>
										<TableCell>
											{DELIVERY_STATUS_LABELS[delivery.status]}
										</TableCell>
										<TableCell>{delivery.attempts}</TableCell>
										<TableCell>
											{delivery.sent_at
												? formatRelativeTime(delivery.sent_at)
												: '—'}
										</TableCell>
									</TableRow>
								))}
							</TableBody>
						</Table>
					)}
				</div>
			) : null}
		</section>
	);
}
