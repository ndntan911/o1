import {
	Line,
	LineChart as RechartsLineChart,
	ResponsiveContainer,
	Tooltip,
	XAxis,
	YAxis,
} from 'recharts';
import type { ProbeResult } from '@/lib/api/types';

export interface LatencyChartProps {
	results: ProbeResult[];
}

const timeFormatter = new Intl.DateTimeFormat('en', {
	month: 'short',
	day: 'numeric',
	hour: '2-digit',
	minute: '2-digit',
});

interface LatencyPoint {
	timestamp: number;
	label: string;
	latency: number;
}

const axisTick = { fontSize: 12, fill: 'var(--muted-foreground)' };

function toPoints(results: ProbeResult[]): LatencyPoint[] {
	return results
		.map((result) => {
			const timestamp = new Date(result.checked_at).getTime();
			return {
				timestamp,
				label: timeFormatter.format(timestamp),
				latency: Math.round(result.latency_ms),
			};
		})
		.filter((point) => Number.isFinite(point.timestamp))
		.sort((left, right) => left.timestamp - right.timestamp);
}

export function LatencyChart({ results }: LatencyChartProps) {
	const points = toPoints(results);

	if (points.length === 0) {
		return (
			<section className="space-y-2">
				<h2 className="font-heading text-lg font-medium">Latency</h2>
				<p className="text-sm text-muted-foreground">
					No latency data in this window
				</p>
			</section>
		);
	}

	return (
		<section className="space-y-2">
			<h2 className="font-heading text-lg font-medium">Latency</h2>
			<figure aria-label="Latency over the loaded results">
				<ResponsiveContainer width="100%" height={240}>
					<RechartsLineChart
						data={points}
						margin={{ top: 8, right: 8, bottom: 0, left: 0 }}
					>
						<XAxis dataKey="label" tick={axisTick} minTickGap={24} />
						<YAxis tick={axisTick} width={56} unit=" ms" domain={[0, 'auto']} />
						<Tooltip
							contentStyle={{
								backgroundColor: 'var(--popover)',
								borderColor: 'var(--border)',
								color: 'var(--popover-foreground)',
								borderRadius: 'var(--radius-md)',
							}}
							labelStyle={{ color: 'var(--popover-foreground)' }}
							itemStyle={{ color: 'var(--popover-foreground)' }}
						/>
						<Line
							type="monotone"
							dataKey="latency"
							name="Latency"
							stroke="var(--chart-2)"
							strokeWidth={2}
							dot={false}
							isAnimationActive={false}
						/>
					</RechartsLineChart>
				</ResponsiveContainer>
				<figcaption className="sr-only">
					Latency over the loaded results
				</figcaption>
			</figure>
		</section>
	);
}
