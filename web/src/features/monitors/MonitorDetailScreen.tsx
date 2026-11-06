import { lazy, Suspense, useState } from 'react';
import { Link, useNavigate, useSearch } from '@tanstack/react-router';
import { PauseIcon, PencilIcon, PlayIcon, Trash2Icon } from 'lucide-react';
import { ConfirmDialog } from '@/components/feedback/ConfirmDialog';
import { ErrorPanel } from '@/components/feedback/ErrorPanel';
import { ProblemAlert } from '@/components/feedback/ProblemAlert';
import { PageSkeleton } from '@/components/feedback/Skeletons';
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Label } from '@/components/ui/label';
import { Skeleton } from '@/components/ui/skeleton';
import { Switch } from '@/components/ui/switch';
import {
	useMonitorAlertChannel,
	useSetMonitorAlertChannel,
} from '@/features/alerts/hooks';
import { useIncidents } from '@/features/incidents/hooks';
import {
	formatIncidentDuration,
	incidentStatusLabel,
} from '@/features/incidents/status';
import { isApiProblem } from '@/lib/api/client';
import { problemFromError } from '@/lib/api/problem-from-error';
import type { ApiProblem } from '@/lib/api/problems';
import type {
	Monitor,
	MonitorStatus,
	ResultsWindow,
	Workspace,
} from '@/lib/api/types';
import { formatLatency, formatRelativeTime } from '@/lib/format';
import {
	canManageAlertChannels,
	canManageMonitorAlerts,
	canManageMonitors,
} from '@/lib/roles';
import {
	useDeleteMonitor,
	useMonitor,
	usePauseMonitor,
	useResumeMonitor,
	useResults,
	useSummary,
} from './hooks';
import { ResultsTable } from './ResultsTable';
import { statusLabel, statusVariant } from './status';
import { SummaryCards } from './SummaryCards';
import { parseMonitorWindow, RESULTS_WINDOWS } from './window';

const LatencyChart = lazy(async () => {
	const module = await import('./LatencyChart');
	return { default: module.LatencyChart };
});

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

function displayStatus(monitor: Monitor): MonitorStatus {
	return monitor.paused ? 'paused' : monitor.status;
}

function lastCheckLabel(monitor: Monitor): string {
	if (!monitor.last_check) return 'Never checked';
	return formatRelativeTime(monitor.last_check.checked_at);
}

export interface MonitorDetailScreenProps {
	workspace: Workspace;
	monitorId: string;
}

export function MonitorDetailScreen({
	workspace,
	monitorId,
}: MonitorDetailScreenProps) {
	const monitorQuery = useMonitor(workspace.id, monitorId);

	if (monitorQuery.isPending) return <PageSkeleton />;

	if (!monitorQuery.data) {
		const notFound =
			isApiProblem(monitorQuery.error) &&
			monitorQuery.error.problem.code === 'not_found';
		return (
			<ErrorPanel
				title={notFound ? 'Monitor not found' : 'Could not load monitor'}
				message={
					notFound
						? 'This monitor may have been deleted.'
						: errorMessage(monitorQuery.error)
				}
				problem={problemFromError(monitorQuery.error)}
				onRetry={
					!notFound && isRetryable(monitorQuery.error)
						? () => void monitorQuery.refetch()
						: undefined
				}
			/>
		);
	}

	return (
		<MonitorDetailView
			key={monitorId}
			workspace={workspace}
			monitorId={monitorId}
			monitor={monitorQuery.data}
		/>
	);
}

interface MonitorDetailViewProps {
	workspace: Workspace;
	monitorId: string;
	monitor: Monitor;
}

function MonitorDetailView({
	workspace,
	monitorId,
	monitor,
}: MonitorDetailViewProps) {
	const navigate = useNavigate();
	const search = useSearch({ strict: false }) as { window?: unknown };
	const timeWindow = parseMonitorWindow(search.window);

	const summaryQuery = useSummary(workspace.id, monitorId, timeWindow);
	const resultsQuery = useResults(workspace.id, monitorId, timeWindow);
	const recentIncidentsQuery = useIncidents(
		workspace.id,
		{ status: 'all', monitorId },
		5,
	);
	const pause = usePauseMonitor(workspace.id, monitorId);
	const resume = useResumeMonitor(workspace.id, monitorId);
	const remove = useDeleteMonitor(workspace.id, monitorId, workspace.slug);
	const canManageAlerts = canManageMonitorAlerts(workspace.role);
	const alertQuery = useMonitorAlertChannel(
		workspace.id,
		monitorId,
		canManageAlerts,
	);
	const setAlert = useSetMonitorAlertChannel(workspace.id, monitorId);
	const [problem, setProblem] = useState<ApiProblem | null>(null);
	const [alertProblem, setAlertProblem] = useState<ApiProblem | null>(null);
	const [deleteProblem, setDeleteProblem] = useState<ApiProblem | null>(null);
	const [confirmOpen, setConfirmOpen] = useState(false);

	const paused = monitor.paused || monitor.status === 'paused';
	const status = displayStatus(monitor);
	const canManage = canManageMonitors(workspace.role);
	const mutation = paused ? resume : pause;
	const mutationPending = pause.isPending || resume.isPending;
	const results =
		resultsQuery.data?.pages.flatMap((page) => page.results) ?? [];
	const recentIncidents =
		recentIncidentsQuery.data?.pages
			.flatMap((page) => page.incidents)
			.slice(0, 5) ?? [];
	const activeIncident =
		recentIncidents.find((incident) => incident.status !== 'resolved') ?? null;
	const alertState = alertQuery.data;
	const alertSwitchDisabled =
		alertQuery.isPending ||
		alertQuery.isError ||
		!alertState?.available ||
		!alertState.channel_enabled ||
		setAlert.isPending;

	function handleProblem(error: unknown) {
		setProblem(problemFromError(error));
	}

	function runPauseResume() {
		setProblem(null);
		mutation.mutate(undefined, {
			onError: handleProblem,
			onSuccess: () => setProblem(null),
		});
	}

	function runAlertToggle(enabled: boolean) {
		setAlertProblem(null);
		setAlert.mutate(enabled, {
			onError: (error) => setAlertProblem(problemFromError(error)),
			onSuccess: () => setAlertProblem(null),
		});
	}

	function runDelete() {
		setDeleteProblem(null);
		remove.mutate(undefined, {
			onError: (error) => setDeleteProblem(problemFromError(error)),
			onSuccess: () => setDeleteProblem(null),
		});
	}

	function selectWindow(next: ResultsWindow) {
		if (next === timeWindow) return;
		void navigate({
			to: '/w/$workspaceSlug/monitors/$monitorId',
			params: { workspaceSlug: workspace.slug, monitorId },
			search: { window: next },
			replace: true,
		});
	}

	return (
		<section className="space-y-6">
			<Link
				to="/w/$workspaceSlug/monitors"
				params={{ workspaceSlug: workspace.slug }}
				className="text-sm text-muted-foreground underline underline-offset-4"
			>
				Back to monitors
			</Link>

			<div className="flex flex-wrap items-start justify-between gap-4">
				<div className="space-y-1">
					<div className="flex flex-wrap items-center gap-2">
						<h1 className="font-heading text-xl font-semibold">
							{monitor.name}
						</h1>
						<Badge variant={statusVariant(status)}>{statusLabel(status)}</Badge>
					</div>
					<p className="break-all font-mono text-xs text-muted-foreground">
						{monitor.url}
					</p>
					<p className="text-sm text-muted-foreground">
						<span>Last check: </span>
						<span>{lastCheckLabel(monitor)}</span>
						{monitor.last_check ? (
							<>
								<span className="ml-2 text-xs">
									{formatLatency(monitor.last_check.latency_ms)}
								</span>
								{monitor.last_check.status_code !== null ? (
									<span className="ml-2 text-xs">
										HTTP {monitor.last_check.status_code}
									</span>
								) : null}
								{monitor.last_check.error ? (
									<span
										className="ml-2 inline-block max-w-64 truncate align-bottom text-xs text-destructive"
										title={monitor.last_check.error}
									>
										{monitor.last_check.error}
									</span>
								) : null}
							</>
						) : null}
					</p>
				</div>

				{canManage ? (
					<div className="flex flex-wrap items-center gap-2">
						<Button
							type="button"
							variant="outline"
							size="sm"
							aria-label={`${paused ? 'Resume' : 'Pause'} ${monitor.name}`}
							disabled={mutationPending}
							onClick={runPauseResume}
						>
							{paused ? <PlayIcon aria-hidden /> : <PauseIcon aria-hidden />}
						</Button>
						<Button asChild variant="outline" size="sm">
							<Link
								to="/w/$workspaceSlug/monitors/$monitorId/edit"
								params={{ workspaceSlug: workspace.slug, monitorId }}
							>
								<PencilIcon aria-hidden />
								Edit
							</Link>
						</Button>
						<Button
							type="button"
							variant="destructive"
							size="sm"
							disabled={remove.isPending}
							onClick={() => {
								setDeleteProblem(null);
								setConfirmOpen(true);
							}}
						>
							<Trash2Icon aria-hidden />
							Delete
						</Button>
					</div>
				) : null}
			</div>

			{problem ? <ProblemAlert problem={problem} /> : null}

			{activeIncident ? (
				<Alert>
					<AlertTitle>Active incident</AlertTitle>
					<AlertDescription className="space-y-2">
						<p>
							{incidentStatusLabel(activeIncident.status)} incident started{' '}
							{formatRelativeTime(activeIncident.started_at)}.
						</p>
						<Link
							to="/w/$workspaceSlug/incidents/$incidentId"
							params={{
								workspaceSlug: workspace.slug,
								incidentId: activeIncident.id,
							}}
							className="font-medium underline underline-offset-4"
						>
							View incident
						</Link>
					</AlertDescription>
				</Alert>
			) : null}

			{canManageAlerts ? (
				<section
					aria-label="Email alerts"
					className="space-y-3 rounded-xl border p-4"
				>
					<div className="flex flex-wrap items-center justify-between gap-4">
						<div className="space-y-1">
							<Label htmlFor="monitor-email-alerts">Email alerts</Label>
							<p className="text-sm text-muted-foreground">
								Send incident emails for this monitor.
							</p>
						</div>
						<Switch
							id="monitor-email-alerts"
							aria-label="Email alerts"
							checked={alertState?.enabled ?? false}
							disabled={alertSwitchDisabled}
							onCheckedChange={runAlertToggle}
						/>
					</div>

					{alertQuery.isError ? (
						<ProblemAlert problem={problemFromError(alertQuery.error)} />
					) : null}
					{alertProblem ? <ProblemAlert problem={alertProblem} /> : null}

					{alertState && !alertState.available ? (
						<p className="text-sm text-muted-foreground">
							Email alerts are not configured for this workspace.{' '}
							{canManageAlertChannels(workspace.role) ? (
								<Link
									to="/w/$workspaceSlug/settings/alerts"
									params={{ workspaceSlug: workspace.slug }}
									className="font-medium underline underline-offset-4"
								>
									Configure email alerts
								</Link>
							) : null}
						</p>
					) : null}
					{alertState?.available && !alertState.channel_enabled ? (
						<p className="text-sm text-muted-foreground">
							Email alerts are disabled for this workspace.
						</p>
					) : null}
				</section>
			) : null}

			<div className="flex flex-wrap items-center justify-between gap-3">
				<h2 className="font-heading text-lg font-medium">Summary</h2>
				<div
					role="group"
					aria-label="Time window"
					className="inline-flex items-center gap-1 rounded-lg bg-muted p-1"
				>
					{RESULTS_WINDOWS.map((windowOption) => (
						<Button
							key={windowOption}
							type="button"
							size="sm"
							variant={windowOption === timeWindow ? 'secondary' : 'ghost'}
							aria-pressed={windowOption === timeWindow}
							onClick={() => selectWindow(windowOption)}
						>
							{windowOption}
						</Button>
					))}
				</div>
			</div>

			{summaryQuery.isError ? (
				<ErrorPanel
					title="Could not load summary"
					message={errorMessage(summaryQuery.error)}
					problem={problemFromError(summaryQuery.error)}
					onRetry={
						isRetryable(summaryQuery.error)
							? () => void summaryQuery.refetch()
							: undefined
					}
				/>
			) : null}

			{summaryQuery.isError && !summaryQuery.data ? null : (
				<SummaryCards summary={summaryQuery.data} />
			)}

			<Suspense
				fallback={
					<p className="text-sm text-muted-foreground" role="status">
						Loading chart…
					</p>
				}
			>
				<LatencyChart results={results} />
			</Suspense>

			{resultsQuery.isError && results.length === 0 ? (
				<ErrorPanel
					title="Could not load results"
					message={errorMessage(resultsQuery.error)}
					problem={problemFromError(resultsQuery.error)}
					onRetry={
						isRetryable(resultsQuery.error)
							? () => void resultsQuery.refetch()
							: undefined
					}
				/>
			) : resultsQuery.isPending ? (
				<p className="text-sm text-muted-foreground" role="status">
					Loading results…
				</p>
			) : (
				<>
					{resultsQuery.isError ? (
						<ErrorPanel
							title="Could not refresh results"
							message={errorMessage(resultsQuery.error)}
							problem={problemFromError(resultsQuery.error)}
							onRetry={
								isRetryable(resultsQuery.error)
									? () => void resultsQuery.refetch()
									: undefined
							}
						/>
					) : null}
					<ResultsTable
						results={results}
						hasNextPage={resultsQuery.hasNextPage}
						isFetchingNextPage={resultsQuery.isFetchingNextPage}
						onLoadMore={() => void resultsQuery.fetchNextPage()}
					/>
				</>
			)}

			<section className="space-y-3" aria-label="Recent incidents">
				<h2 className="font-heading text-lg font-medium">Recent incidents</h2>
				{recentIncidentsQuery.isPending ? (
					<div
						role="status"
						aria-label="Loading incidents"
						className="space-y-2"
					>
						<Skeleton className="h-11 w-full" />
						<Skeleton className="h-11 w-full" />
					</div>
				) : recentIncidentsQuery.isError && recentIncidents.length === 0 ? (
					<ErrorPanel
						title="Could not load incidents"
						message={errorMessage(recentIncidentsQuery.error)}
						problem={problemFromError(recentIncidentsQuery.error)}
						onRetry={
							isRetryable(recentIncidentsQuery.error)
								? () => void recentIncidentsQuery.refetch()
								: undefined
						}
					/>
				) : recentIncidents.length === 0 ? (
					<p className="text-sm text-muted-foreground">No incidents yet</p>
				) : (
					<ul className="space-y-2">
						{recentIncidents.map((incident) => (
							<li
								key={incident.id}
								className="flex flex-wrap items-center justify-between gap-2 rounded-lg border px-3 py-2 text-sm"
							>
								<Link
									to="/w/$workspaceSlug/incidents/$incidentId"
									params={{
										workspaceSlug: workspace.slug,
										incidentId: incident.id,
									}}
									className="font-medium underline underline-offset-4"
								>
									{incidentStatusLabel(incident.status)}
								</Link>
								<span className="text-xs text-muted-foreground">
									Started {formatRelativeTime(incident.started_at)} ·{' '}
									{formatIncidentDuration(
										incident.started_at,
										incident.resolved_at,
									)}
								</span>
							</li>
						))}
					</ul>
				)}
			</section>

			<ConfirmDialog
				open={confirmOpen}
				title={`Delete ${monitor.name}?`}
				description="This permanently deletes the monitor and its results."
				confirmLabel="Delete monitor"
				pending={remove.isPending}
				problem={deleteProblem}
				onConfirm={runDelete}
				onOpenChange={(open) => {
					setConfirmOpen(open);
					if (!open) setDeleteProblem(null);
				}}
			/>
		</section>
	);
}
