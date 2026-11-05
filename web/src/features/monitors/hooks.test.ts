import { describe, expect, test } from 'vitest';
import type { Monitor, MonitorListResponse } from '@/lib/api/types';
import { updateMonitorInList } from './hooks';

function monitor(overrides: Partial<Monitor> = {}): Monitor {
	return {
		id: 'm1',
		workspace_id: 'w1',
		name: 'API',
		url: 'https://api.example.com/health',
		method: 'GET',
		headers: {},
		has_body: false,
		content_type: null,
		expected_status: '2xx',
		interval_seconds: 60,
		timeout_seconds: 10,
		confirmation_threshold: 1,
		recovery_threshold: 1,
		paused: false,
		status: 'up',
		last_check: null,
		summary_24h: null,
		created_at: '2026-01-01T00:00:00Z',
		updated_at: '2026-01-01T00:00:00Z',
		...overrides,
	};
}

describe('updateMonitorInList', () => {
	test('patches the matching monitor in every page and leaves others untouched', () => {
		const first = monitor({ id: 'm1', name: 'First' });
		const target = monitor({ id: 'm2', name: 'Target' });
		const last = monitor({ id: 'm3', name: 'Last' });
		const pages: MonitorListResponse[] = [
			{ monitors: [first, target], next_cursor: 'page-2' },
			{ monitors: [last] },
		];

		const updated = updateMonitorInList(pages, 'm2', {
			paused: true,
			status: 'paused',
		});

		expect(updated).toHaveLength(2);
		expect(updated[0].next_cursor).toBe('page-2');
		expect(updated[0].monitors[0]).toBe(first);
		expect(updated[0].monitors[1]).toEqual({
			...target,
			paused: true,
			status: 'paused',
		});
		expect(updated[1]).toEqual(pages[1]);
		expect(pages[0].monitors[1]).toBe(target);
		expect(target.status).toBe('up');
	});

	test('returns equivalent pages when the monitor is absent', () => {
		const pages: MonitorListResponse[] = [
			{ monitors: [monitor({ id: 'm1' })] },
		];

		const updated = updateMonitorInList(pages, 'missing', {
			status: 'down',
		});

		expect(updated).toEqual(pages);
		expect(updated).not.toBe(pages);
	});
});
