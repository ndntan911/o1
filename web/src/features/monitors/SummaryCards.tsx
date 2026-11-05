import type { Summary } from '@/lib/api/types';
import { formatLatency, formatUptime } from '@/lib/format';

export interface SummaryCardsProps {
	summary: Summary | null | undefined;
}

function countValue(value: number | null | undefined): string {
	return value === null || value === undefined ? '—' : String(value);
}

export function SummaryCards({ summary }: SummaryCardsProps) {
	const metrics = [
		{
			label: 'Uptime',
			value: formatUptime(summary?.uptime, summary?.checks),
		},
		{ label: 'Checks', value: countValue(summary?.checks) },
		{ label: 'Up', value: countValue(summary?.up) },
		{ label: 'Down', value: countValue(summary?.down) },
		{
			label: 'Avg latency',
			value: formatLatency(summary?.avg_latency_ms),
		},
		{
			label: 'p95 latency',
			value: formatLatency(summary?.p95_latency_ms),
		},
	];

	return (
		<dl className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-6">
			{metrics.map((metric) => (
				<div
					key={metric.label}
					className="rounded-xl bg-card p-4 text-sm text-card-foreground ring-1 ring-foreground/10"
				>
					<dt className="text-muted-foreground">{metric.label}</dt>
					<dd className="font-heading text-2xl font-semibold tabular-nums">
						{metric.value}
					</dd>
				</div>
			))}
		</dl>
	);
}
