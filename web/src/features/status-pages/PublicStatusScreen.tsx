/* eslint-disable react-refresh/only-export-components -- the exported status helpers are unit tested alongside the screen. */
import { useEffect, type ReactNode } from 'react';
import { EmptyState } from '@/components/feedback/EmptyState';
import { PageSkeleton } from '@/components/feedback/Skeletons';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { isApiProblem } from '@/lib/api/client';
import type {
	PublicMonitorStatus,
	PublicStatusDay,
	PublicStatusDayStatus,
	PublicStatusIncident,
	PublicStatusMonitor,
} from '@/lib/api/types';
import { usePublicStatus } from './hooks';

export interface PublicStatusScreenProps {
	slug: string;
}

const MONITOR_STATUS_LABELS: Record<PublicMonitorStatus, string> = {
	up: 'Up',
	down: 'Down',
	unknown: 'Unknown',
	paused: 'Paused',
};

const DAY_STATUS_LABELS: Record<PublicStatusDayStatus, string> = {
	up: 'up',
	partial: 'partial',
	down: 'down',
	no_data: 'no data',
};

const DAY_STATUS_CLASSES: Record<PublicStatusDayStatus, string> = {
	up: 'bg-emerald-500',
	partial: 'bg-amber-400',
	down: 'bg-red-500',
	no_data: 'bg-muted-foreground/25',
};

const DAY_LEGEND: { status: PublicStatusDayStatus; label: string }[] = [
	{ status: 'up', label: 'Up' },
	{ status: 'partial', label: 'Partial' },
	{ status: 'down', label: 'Down' },
	{ status: 'no_data', label: 'No data' },
];

const BANNER_CLASSES = {
	ok: 'border-emerald-600/20 bg-emerald-50 text-emerald-900 dark:bg-emerald-950/40 dark:text-emerald-100',
	issue: 'border-destructive/30 bg-destructive/10 text-destructive',
	paused: 'border-border bg-muted text-muted-foreground',
} as const;

export function formatUptime(value: number | null): string {
	if (value === null) return 'No data';
	return `${(value * 100).toFixed(2)}%`;
}

export type BannerTone = 'ok' | 'issue' | 'paused' | 'none';

export interface OverallBanner {
	tone: BannerTone;
	text: string;
}

export function overallBanner(monitors: PublicStatusMonitor[]): OverallBanner {
	if (monitors.length === 0) return { tone: 'none', text: '' };
	if (monitors.some((monitor) => monitor.status === 'down')) {
		return { tone: 'issue', text: 'Some systems are experiencing issues' };
	}
	if (monitors.every((monitor) => monitor.status === 'paused')) {
		return { tone: 'paused', text: 'Status updates are paused' };
	}
	return { tone: 'ok', text: 'All systems operational' };
}

export function incidentDuration(incident: PublicStatusIncident): string {
	const startedAt = new Date(incident.started_at).getTime();
	const endedAt = incident.resolved_at
		? new Date(incident.resolved_at).getTime()
		: Date.now();
	const totalMinutes = Math.max(0, Math.floor((endedAt - startedAt) / 60_000));
	const hours = Math.floor(totalMinutes / 60);
	const minutes = totalMinutes % 60;

	if (hours === 0) return `${minutes}m`;
	return minutes > 0 ? `${hours}h ${minutes}m` : `${hours}h`;
}

const dayFormatter = new Intl.DateTimeFormat('en-US', {
	month: 'long',
	day: 'numeric',
	timeZone: 'UTC',
});

const timestampFormatter = new Intl.DateTimeFormat('en-US', {
	dateStyle: 'medium',
	timeStyle: 'short',
	timeZone: 'UTC',
});

function formatDayName(date: string): string {
	return dayFormatter.format(new Date(`${date}T00:00:00Z`));
}

function formatTimestamp(iso: string): string {
	return timestampFormatter.format(new Date(iso));
}

function dayTitle(day: PublicStatusDay): string {
	return `${day.date}: ${day.uptime === null ? 'No data' : formatUptime(day.uptime)}`;
}

function monitorUptimeSummary(monitor: PublicStatusMonitor): string {
	if (monitor.uptime === null) return 'No data over the last 90 days';
	return `${formatUptime(monitor.uptime)} uptime over the last 90 days`;
}

function OverallBannerView({ banner }: { banner: OverallBanner }) {
	if (banner.tone === 'none') return null;

	return (
		<div
			role="status"
			className={`rounded-lg border px-4 py-3 text-sm font-medium ${BANNER_CLASSES[banner.tone]}`}
		>
			{banner.text}
		</div>
	);
}

function DayBars({ monitor }: { monitor: PublicStatusMonitor }) {
	const nonUpDays = monitor.days.filter((day) => day.status !== 'up');

	return (
		<>
			<div className="flex h-8 items-stretch gap-px" aria-hidden="true">
				{monitor.days.map((day) => (
					<span
						key={day.date}
						data-testid="status-day"
						aria-hidden="true"
						title={dayTitle(day)}
						className={`min-w-0 flex-1 rounded-[2px] ${DAY_STATUS_CLASSES[day.status]}`}
					/>
				))}
			</div>
			<ul className="sr-only" aria-label={`${monitor.name} daily history`}>
				{nonUpDays.map((day) => (
					<li key={day.date}>
						{formatDayName(day.date)}: {DAY_STATUS_LABELS[day.status]}
					</li>
				))}
			</ul>
		</>
	);
}

function MonitorRow({ monitor }: { monitor: PublicStatusMonitor }) {
	const statusVariant = monitor.status === 'down' ? 'destructive' : 'outline';

	return (
		<li className="space-y-3 rounded-xl border bg-card p-4">
			<div className="flex flex-wrap items-center justify-between gap-2">
				<h2 className="font-heading text-base font-medium">{monitor.name}</h2>
				<Badge
					variant={statusVariant}
					className={
						monitor.status === 'up' ? 'bg-emerald-600 text-white' : undefined
					}
				>
					{MONITOR_STATUS_LABELS[monitor.status]}
				</Badge>
			</div>
			<p className="text-sm text-muted-foreground">
				{monitorUptimeSummary(monitor)}
			</p>
			<DayBars monitor={monitor} />
		</li>
	);
}

function DayLegend() {
	return (
		<ul
			aria-label="Daily status legend"
			className="flex flex-wrap gap-4 text-xs"
		>
			{DAY_LEGEND.map((entry) => (
				<li key={entry.status} className="flex items-center gap-1.5">
					<span
						aria-hidden="true"
						className={`size-2.5 rounded-[2px] ${DAY_STATUS_CLASSES[entry.status]}`}
					/>
					<span className="text-muted-foreground">{entry.label}</span>
				</li>
			))}
		</ul>
	);
}

function IncidentList({
	heading,
	incidents,
	emptyText,
}: {
	heading: string;
	incidents: PublicStatusIncident[];
	emptyText: string;
}) {
	return (
		<section className="space-y-3">
			<h2 className="font-heading text-lg font-semibold">{heading}</h2>
			{incidents.length === 0 ? (
				<p className="text-sm text-muted-foreground">{emptyText}</p>
			) : (
				<ul className="space-y-2">
					{incidents.map((incident) => (
						<li
							key={incident.id}
							className="space-y-1 rounded-xl border bg-card p-4 text-sm"
						>
							<div className="font-medium">{incident.monitor_name}</div>
							<p className="text-muted-foreground">
								Started {formatTimestamp(incident.started_at)}
							</p>
							{incident.resolved_at ? (
								<p className="text-muted-foreground">
									Resolved {formatTimestamp(incident.resolved_at)} ·{' '}
									{incidentDuration(incident)}
								</p>
							) : null}
						</li>
					))}
				</ul>
			)}
		</section>
	);
}

function errorMessage(error: unknown): string {
	if (isApiProblem(error)) {
		return error.problem.detail ?? error.problem.title;
	}
	if (error instanceof Error) return error.message;
	return 'Please try again.';
}

function Shell({ children }: { children: ReactNode }) {
	return (
		<div className="min-h-screen bg-muted/30">
			<main className="mx-auto w-full max-w-5xl space-y-8 px-4 py-10 sm:px-6">
				<p className="text-sm font-medium tracking-tight text-muted-foreground">
					o1 uptime
				</p>
				{children}
			</main>
		</div>
	);
}

export function PublicStatusScreen({ slug }: PublicStatusScreenProps) {
	const statusQuery = usePublicStatus(slug);
	const pageTitle = statusQuery.data?.page.title;

	useEffect(() => {
		if (!pageTitle) return;
		const previousTitle = document.title;
		document.title = `${pageTitle} · o1 uptime`;
		return () => {
			document.title = previousTitle;
		};
	}, [pageTitle]);

	if (statusQuery.isPending) {
		return (
			<Shell>
				<PageSkeleton />
			</Shell>
		);
	}

	if (statusQuery.isError) {
		if (
			isApiProblem(statusQuery.error) &&
			statusQuery.error.problem.code === 'not_found'
		) {
			return (
				<Shell>
					<div className="space-y-2">
						<h1 className="font-heading text-2xl font-semibold tracking-tight">
							Status page not found
						</h1>
						<p className="text-sm text-muted-foreground">
							This status page does not exist or is not published.
						</p>
					</div>
				</Shell>
			);
		}

		return (
			<Shell>
				<div className="space-y-3 rounded-xl border border-destructive/30 bg-card p-6">
					<h1 className="font-heading text-xl font-semibold">
						Status page unavailable
					</h1>
					<p className="text-sm text-muted-foreground">
						{errorMessage(statusQuery.error)}
					</p>
					<Button
						type="button"
						variant="outline"
						size="sm"
						onClick={() => void statusQuery.refetch()}
					>
						Try again
					</Button>
				</div>
			</Shell>
		);
	}

	const data = statusQuery.data;
	const banner = overallBanner(data.monitors);

	return (
		<Shell>
			<header className="space-y-4">
				<div className="space-y-2">
					<h1 className="font-heading text-3xl font-semibold tracking-tight">
						{data.page.title}
					</h1>
					{data.page.description ? (
						<p className="text-muted-foreground">{data.page.description}</p>
					) : null}
				</div>
				<OverallBannerView banner={banner} />
			</header>

			{data.monitors.length === 0 ? (
				<EmptyState title="No monitors are currently published." />
			) : (
				<section aria-label="Monitors" className="space-y-3">
					<ul className="space-y-3">
						{data.monitors.map((monitor) => (
							<MonitorRow key={monitor.id} monitor={monitor} />
						))}
					</ul>
					<DayLegend />
				</section>
			)}

			<div className="space-y-6">
				<IncidentList
					heading="Active incidents"
					incidents={data.incidents.active}
					emptyText="No active incidents."
				/>
				<IncidentList
					heading="Past incidents"
					incidents={data.incidents.recent}
					emptyText="No incidents in the last 7 days."
				/>
			</div>
		</Shell>
	);
}
