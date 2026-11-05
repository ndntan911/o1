import type { ReactNode } from 'react';
import { render, screen } from '@testing-library/react';
import { describe, expect, test, vi } from 'vitest';
import type { ProbeResult } from '@/lib/api/types';
import { LatencyChart } from './LatencyChart';

interface CapturedPoint {
	timestamp: number;
	latency: number;
}

const captured = vi.hoisted(() => ({ points: [] as CapturedPoint[] }));

vi.mock('recharts', () => ({
	ResponsiveContainer: ({ children }: { children?: ReactNode }) => (
		<div>{children}</div>
	),
	LineChart: ({
		data,
		children,
	}: {
		data?: CapturedPoint[];
		children?: ReactNode;
	}) => {
		captured.points = data ?? [];
		return <div data-testid="line-chart">{children}</div>;
	},
	XAxis: () => null,
	YAxis: () => null,
	Tooltip: () => null,
	Line: () => null,
}));

const EARLIER = '2026-01-01T10:00:00Z';
const MIDDLE = '2026-01-01T11:00:00Z';
const LATER = '2026-01-01T12:00:00Z';

function result(id: string, checkedAt: string, latency: number): ProbeResult {
	return {
		id,
		checked_at: checkedAt,
		scheduled_for: checkedAt,
		status: 'up',
		status_code: 200,
		latency_ms: latency,
		error: null,
	};
}

describe('LatencyChart', () => {
	test('plots the loaded results in chronological order', () => {
		render(
			<LatencyChart
				results={[
					result('later', LATER, 300.4),
					result('earlier', EARLIER, 100.6),
					result('middle', MIDDLE, 200),
				]}
			/>,
		);

		expect(captured.points.map((point) => point.timestamp)).toEqual([
			new Date(EARLIER).getTime(),
			new Date(MIDDLE).getTime(),
			new Date(LATER).getTime(),
		]);
		expect(captured.points.map((point) => point.latency)).toEqual([
			101, 200, 300,
		]);
		expect(
			screen.getByText('Latency over the loaded results'),
		).toBeInTheDocument();
		expect(
			screen.getByRole('heading', { name: 'Latency' }),
		).toBeInTheDocument();
	});

	test('shows an empty state when there are no points', () => {
		render(<LatencyChart results={[]} />);

		expect(
			screen.getByText('No latency data in this window'),
		).toBeInTheDocument();
		expect(screen.queryByTestId('line-chart')).not.toBeInTheDocument();
	});
});
