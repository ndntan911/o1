import { describe, expect, test } from 'vitest';
import { formatLatency, formatRelativeTime, formatUptime } from './format';

describe('formatUptime', () => {
	test('renders no data for null or zero checks', () => {
		expect(formatUptime(null, 0)).toBe('No data');
		expect(formatUptime(0.99, 0)).toBe('No data');
	});
	test('renders one decimal', () => {
		expect(formatUptime(0.999, 10)).toBe('99.9%');
		expect(formatUptime(1, 10)).toBe('100.0%');
	});
});

describe('formatLatency', () => {
	test('rounds and labels milliseconds', () => {
		expect(formatLatency(12.6)).toBe('13 ms');
		expect(formatLatency(null)).toBe('—');
	});
});

describe('formatRelativeTime', () => {
	test('uses a relative hint against a fixed clock', () => {
		const now = new Date('2026-11-05T12:00:00Z');
		expect(formatRelativeTime('2026-11-05T11:58:00Z', now)).toBe(
			'2 minutes ago',
		);
	});
});
