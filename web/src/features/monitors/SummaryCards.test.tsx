import { render, screen } from '@testing-library/react';
import { describe, expect, test } from 'vitest';
import type { Summary } from '@/lib/api/types';
import { SummaryCards } from './SummaryCards';

const summary: Summary = {
	window: '24h',
	uptime: 0.987,
	checks: 10,
	up: 9,
	down: 1,
	avg_latency_ms: 120.4,
	p95_latency_ms: 199.6,
	min_latency_ms: 80,
	max_latency_ms: 300,
};

function metricValue(label: string): string | null | undefined {
	return screen.getByText(label).parentElement?.querySelector('dd')
		?.textContent;
}

describe('SummaryCards', () => {
	test('renders No data when uptime is null', () => {
		render(<SummaryCards summary={{ ...summary, uptime: null }} />);

		expect(screen.getAllByText('No data')).toHaveLength(1);
		expect(screen.queryByText(/NaN/)).not.toBeInTheDocument();
		expect(metricValue('Uptime')).toBe('No data');
	});

	test('renders No data when there are zero checks', () => {
		render(
			<SummaryCards
				summary={{ ...summary, checks: 0, uptime: 0, up: 0, down: 0 }}
			/>,
		);

		expect(screen.getAllByText('No data')).toHaveLength(1);
		expect(screen.queryByText('0.0%')).not.toBeInTheDocument();
		expect(screen.queryByText(/NaN/)).not.toBeInTheDocument();
	});

	test('renders No data for a null summary', () => {
		render(<SummaryCards summary={null} />);

		expect(screen.getAllByText('No data')).toHaveLength(1);
		expect(screen.queryByText(/NaN/)).not.toBeInTheDocument();
	});

	test('renders checks, up, down, average latency, and p95 latency', () => {
		render(<SummaryCards summary={summary} />);

		expect(metricValue('Uptime')).toBe('98.7%');
		expect(metricValue('Checks')).toBe('10');
		expect(metricValue('Up')).toBe('9');
		expect(metricValue('Down')).toBe('1');
		expect(metricValue('Avg latency')).toBe('120 ms');
		expect(metricValue('p95 latency')).toBe('200 ms');
	});
});
