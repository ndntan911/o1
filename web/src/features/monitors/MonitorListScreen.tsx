import { useEffect, useMemo, useState } from 'react';
import { Link } from '@tanstack/react-router';
import { EyeIcon, PauseIcon, PlayIcon, Trash2Icon } from 'lucide-react';
import { ConfirmDialog } from '@/components/feedback/ConfirmDialog';
import { EmptyState } from '@/components/feedback/EmptyState';
import { ErrorPanel } from '@/components/feedback/ErrorPanel';
import { ProblemAlert } from '@/components/feedback/ProblemAlert';
import { PageSkeleton } from '@/components/feedback/Skeletons';
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import {
	Card,
	CardContent,
	CardFooter,
	CardHeader,
	CardTitle,
} from '@/components/ui/card';
import { Input } from '@/components/ui/input';
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
import type { Monitor, Workspace } from '@/lib/api/types';
import { formatLatency, formatRelativeTime, formatUptime } from '@/lib/format';
import { canManageMonitors } from '@/lib/roles';
import {
	useDeleteMonitor,
	useMonitors,
	usePauseMonitor,
	useResumeMonitor,
} from './hooks';
import { statusLabel, statusVariant } from './status';

const PAGE_PROBLEM_CODES = new Set([
	'quota_exceeded',
	'monitor_rate_limited',
	'workspace_suspended',
]);

const DESKTOP_QUERY = '(min-width: 768px)';

function useIsDesktop(): boolean {
	const [isDesktop, setIsDesktop] = useState(false);

	useEffect(() => {
		const media = window.matchMedia?.(DESKTOP_QUERY);
		if (!media) return;

		const update = () => setIsDesktop(media.matches);
		update();
		media.addEventListener?.('change', update);
		return () => media.removeEventListener?.('change', update);
	}, []);

	return isDesktop;
}

function pageProblem(error: unknown): ApiProblem | null {
	if (!isApiProblem(error)) return null;
	return PAGE_PROBLEM_CODES.has(error.problem.code) ? error.problem : null;
}

function errorMessage(error: unknown): string {
	if (isApiProblem(error)) {
		return error.problem.detail ?? error.problem.title;
	}
	if (error instanceof Error) return error.message;
	return 'Please try again.';
}

function monitorUptime(monitor: Monitor): string {
	return formatUptime(monitor.summary_24h?.uptime, monitor.summary_24h?.checks);
}

function monitorLastCheck(monitor: Monitor): string {
	if (!monitor.last_check) return 'Never checked';
	return formatRelativeTime(monitor.last_check.checked_at);
}

function monitorHref(workspaceSlug: string, monitorId: string): string {
	return `/w/${encodeURIComponent(workspaceSlug)}/monitors/${encodeURIComponent(monitorId)}`;
}

interface MonitorActionsProps {
	workspace: Workspace;
	monitor: Monitor;
	onProblem: (error: unknown) => void;
}

function MonitorActions({
	workspace,
	monitor,
	onProblem,
}: MonitorActionsProps) {
	const [confirmOpen, setConfirmOpen] = useState(false);
	const pause = usePauseMonitor(workspace.id, monitor.id);
	const resume = useResumeMonitor(workspace.id, monitor.id);
	const remove = useDeleteMonitor(workspace.id, monitor.id, workspace.slug);
	const paused = monitor.paused || monitor.status === 'paused';
	const mutation = paused ? resume : pause;
	const busy = pause.isPending || resume.isPending;

	return (
		<div className="flex items-center justify-end gap-1">
			<Button asChild variant="ghost" size="icon-sm">
				<a
					href={monitorHref(workspace.slug, monitor.id)}
					aria-label={`View ${monitor.name}`}
				>
					<EyeIcon aria-hidden />
				</a>
			</Button>
			{canManageMonitors(workspace.role) ? (
				<>
					<Button
						type="button"
						variant="ghost"
						size="icon-sm"
						aria-label={`${paused ? 'Resume' : 'Pause'} ${monitor.name}`}
						disabled={busy}
						onClick={() => mutation.mutate(undefined, { onError: onProblem })}
					>
						{paused ? <PlayIcon aria-hidden /> : <PauseIcon aria-hidden />}
					</Button>
					<Button
						type="button"
						variant="ghost"
						size="icon-sm"
						aria-label={`Delete ${monitor.name}`}
						disabled={remove.isPending}
						onClick={() => setConfirmOpen(true)}
					>
						<Trash2Icon aria-hidden />
					</Button>
					<ConfirmDialog
						open={confirmOpen}
						title={`Delete ${monitor.name}?`}
						description="This permanently deletes the monitor and its results."
						confirmLabel="Delete monitor"
						pending={remove.isPending}
						onConfirm={() => remove.mutate(undefined, { onError: onProblem })}
						onOpenChange={setConfirmOpen}
					/>
				</>
			) : null}
		</div>
	);
}

export interface MonitorListScreenProps {
	workspace: Workspace;
}

export function MonitorListScreen({ workspace }: MonitorListScreenProps) {
	const monitorsQuery = useMonitors(workspace.id);
	const isDesktop = useIsDesktop();
	const [search, setSearch] = useState('');
	const [problem, setProblem] = useState<ApiProblem | null>(null);

	const monitors = useMemo(
		() => monitorsQuery.data?.pages.flatMap((page) => page.monitors) ?? [],
		[monitorsQuery.data],
	);

	const filtered = useMemo(() => {
		const term = search.trim().toLowerCase();
		if (!term) return monitors;
		return monitors.filter(
			(monitor) =>
				monitor.name.toLowerCase().includes(term) ||
				monitor.url.toLowerCase().includes(term),
		);
	}, [monitors, search]);

	const canManage = canManageMonitors(workspace.role);
	const mutationProblem = problem ?? pageProblem(monitorsQuery.error);
	const listProblem = pageProblem(monitorsQuery.error);

	function handleProblem(error: unknown) {
		const next = pageProblem(error);
		if (next) setProblem(next);
	}

	return (
		<section className="space-y-6">
			<div className="flex flex-wrap items-center justify-between gap-3">
				<div>
					<h1 className="font-heading text-xl font-semibold">Monitors</h1>
					<p className="text-sm text-muted-foreground">
						HTTP uptime checks for {workspace.name}.
					</p>
				</div>
				{canManage && monitors.length > 0 ? (
					<Button asChild>
						<Link
							to="/w/$workspaceSlug/monitors/new"
							params={{ workspaceSlug: workspace.slug }}
						>
							New monitor
						</Link>
					</Button>
				) : null}
			</div>

			<Input
				type="search"
				aria-label="Search monitors"
				placeholder="Search monitors"
				value={search}
				onChange={(event) => setSearch(event.target.value)}
			/>

			{mutationProblem ? <ProblemAlert problem={mutationProblem} /> : null}

			{monitorsQuery.isPending ? (
				<PageSkeleton />
			) : monitorsQuery.isError && monitors.length === 0 ? (
				listProblem ? null : (
					<ErrorPanel
						message={errorMessage(monitorsQuery.error)}
						onRetry={() => void monitorsQuery.refetch()}
					/>
				)
			) : monitors.length === 0 ? (
				<EmptyState
					title="No monitors yet"
					description="Monitors check an HTTP endpoint on a schedule and record its uptime and latency."
					action={
						canManage ? (
							<Button asChild>
								<Link
									to="/w/$workspaceSlug/monitors/new"
									params={{ workspaceSlug: workspace.slug }}
								>
									New monitor
								</Link>
							</Button>
						) : null
					}
				/>
			) : (
				<>
					{monitorsQuery.isError ? (
						<Alert>
							<AlertTitle>Could not refresh monitors</AlertTitle>
							<AlertDescription className="space-y-2">
								<p>Showing the last loaded monitors.</p>
								<Button
									type="button"
									variant="outline"
									size="sm"
									onClick={() => void monitorsQuery.refetch()}
								>
									Retry
								</Button>
							</AlertDescription>
						</Alert>
					) : null}
					{filtered.length === 0 ? (
						<p className="text-sm text-muted-foreground">
							No monitors match your search.
						</p>
					) : isDesktop ? (
						<Table>
							<TableHeader>
								<TableRow>
									<TableHead>Name</TableHead>
									<TableHead>URL</TableHead>
									<TableHead>Status</TableHead>
									<TableHead>Last check</TableHead>
									<TableHead>Uptime</TableHead>
									<TableHead className="text-right">Actions</TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{filtered.map((monitor) => (
									<TableRow key={monitor.id}>
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
											<span>{monitorLastCheck(monitor)}</span>
											{monitor.last_check ? (
												<span className="ml-2 text-xs text-muted-foreground">
													{formatLatency(monitor.last_check.latency_ms)}
												</span>
											) : null}
										</TableCell>
										<TableCell>{monitorUptime(monitor)}</TableCell>
										<TableCell>
											<MonitorActions
												workspace={workspace}
												monitor={monitor}
												onProblem={handleProblem}
											/>
										</TableCell>
									</TableRow>
								))}
							</TableBody>
						</Table>
					) : (
						<div className="space-y-3">
							{filtered.map((monitor) => (
								<Card key={monitor.id}>
									<CardHeader>
										<div className="flex items-start justify-between gap-3">
											<CardTitle>{monitor.name}</CardTitle>
											<Badge variant={statusVariant(monitor.status)}>
												{statusLabel(monitor.status)}
											</Badge>
										</div>
										<p className="truncate font-mono text-xs text-muted-foreground">
											{monitor.url}
										</p>
									</CardHeader>
									<CardContent className="space-y-1 text-sm">
										<p>
											<span className="text-muted-foreground">
												Last check:{' '}
											</span>
											{monitorLastCheck(monitor)}
											{monitor.last_check ? (
												<span className="ml-2 text-xs text-muted-foreground">
													{formatLatency(monitor.last_check.latency_ms)}
												</span>
											) : null}
										</p>
										<p>
											<span className="text-muted-foreground">Uptime: </span>
											{monitorUptime(monitor)}
										</p>
									</CardContent>
									<CardFooter className="justify-end">
										<MonitorActions
											workspace={workspace}
											monitor={monitor}
											onProblem={handleProblem}
										/>
									</CardFooter>
								</Card>
							))}
						</div>
					)}

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
