import { lazy, Suspense, useState } from 'react';
import { Link, useNavigate, useSearch } from '@tanstack/react-router';
import { PauseIcon, PencilIcon, PlayIcon, Trash2Icon } from 'lucide-react';
import { ConfirmDialog } from '@/components/feedback/ConfirmDialog';
import { ErrorPanel } from '@/components/feedback/ErrorPanel';
import { ProblemAlert } from '@/components/feedback/ProblemAlert';
import { PageSkeleton } from '@/components/feedback/Skeletons';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { isApiProblem } from '@/lib/api/client';
import type { ApiProblem } from '@/lib/api/problems';
import type {
	Monitor,
	MonitorStatus,
	ResultsWindow,
	Workspace,
} from '@/lib/api/types';
import { formatLatency, formatRelativeTime } from '@/lib/format';
import { canManageMonitors } from '@/lib/roles';
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

function unexpectedProblem(): ApiProblem {
	return {
		type: 'about:blank',
		title: 'Something went wrong',
		status: 0,
		code: 'internal_error',
		detail: 'Please try again.',
	};
}

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
	const pause = usePauseMonitor(workspace.id, monitorId);
	const resume = useResumeMonitor(workspace.id, monitorId);
	const remove = useDeleteMonitor(workspace.id, monitorId, workspace.slug);
	const [problem, setProblem] = useState<ApiProblem | null>(null);
	const [confirmOpen, setConfirmOpen] = useState(false);

	const paused = monitor.paused || monitor.status === 'paused';
	const status = displayStatus(monitor);
	const canManage = canManageMonitors(workspace.role);
	const mutation = paused ? resume : pause;
	const mutationPending = pause.isPending || resume.isPending;
	const results =
		resultsQuery.data?.pages.flatMap((page) => page.results) ?? [];

	function handleProblem(error: unknown) {
		setProblem(isApiProblem(error) ? error.problem : unexpectedProblem());
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
							onClick={() =>
								mutation.mutate(undefined, { onError: handleProblem })
							}
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
							onClick={() => setConfirmOpen(true)}
						>
							<Trash2Icon aria-hidden />
							Delete
						</Button>
					</div>
				) : null}
			</div>

			{problem ? <ProblemAlert problem={problem} /> : null}

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

			<ConfirmDialog
				open={confirmOpen}
				title={`Delete ${monitor.name}?`}
				description="This permanently deletes the monitor and its results."
				confirmLabel="Delete monitor"
				pending={remove.isPending}
				onConfirm={() => remove.mutate(undefined, { onError: handleProblem })}
				onOpenChange={setConfirmOpen}
			/>
		</section>
	);
}
