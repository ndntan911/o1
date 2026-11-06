import { createElement, type ReactNode } from 'react';
import { describe, expect, test } from 'vitest';
import { act, renderHook, waitFor } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import { HttpResponse, http } from 'msw';
import { server } from '@/test/msw/server';
import { createTestQueryClient } from '@/test/render';
import type { Incident, IncidentDetail } from '@/lib/api/types';
import { queryKeys } from '@/lib/query/keys';
import { useAcknowledgeIncident, useIncidents } from './hooks';

const ORIGIN = 'http://localhost:5173';
const workspaceId = 'w1';
const incidentsPath = `${ORIGIN}/api/v1/workspaces/${workspaceId}/incidents`;

function makeIncident(overrides: Partial<Incident> = {}): Incident {
	return {
		id: 'i1',
		workspace_id: workspaceId,
		monitor_id: 'm1',
		monitor_name: 'API',
		status: 'open',
		started_at: '2026-01-01T00:00:00Z',
		acknowledged_at: null,
		acknowledged_by: null,
		resolved_at: null,
		last_notified_at: null,
		reminder_count: 0,
		...overrides,
	};
}

function wrapperFor(queryClient: ReturnType<typeof createTestQueryClient>) {
	return function Wrapper({ children }: { children: ReactNode }) {
		return createElement(
			QueryClientProvider,
			{ client: queryClient },
			children,
		);
	};
}

describe('incident hooks', () => {
	test('useIncidents sends the status filter, monitor_id, and cursor pages', async () => {
		const requests: URLSearchParams[] = [];
		server.use(
			http.get(incidentsPath, ({ request }) => {
				const url = new URL(request.url);
				requests.push(url.searchParams);
				const cursor = url.searchParams.get('cursor');
				return HttpResponse.json(
					cursor
						? { incidents: [makeIncident({ id: 'i2' })] }
						: { incidents: [makeIncident()], next_cursor: 'page-2' },
				);
			}),
		);
		const queryClient = createTestQueryClient();

		const { result } = renderHook(
			() => useIncidents(workspaceId, { status: 'open', monitorId: 'm1' }),
			{ wrapper: wrapperFor(queryClient) },
		);

		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(requests[0].get('status')).toBe('open');
		expect(requests[0].get('monitor_id')).toBe('m1');
		expect(requests[0].get('limit')).toBe('50');
		expect(requests[0].get('cursor')).toBeNull();
		expect(result.current.hasNextPage).toBe(true);

		await act(async () => {
			await result.current.fetchNextPage();
		});

		await waitFor(() => expect(result.current.data?.pages).toHaveLength(2));
		expect(requests[1].get('cursor')).toBe('page-2');
	});

	test('useAcknowledgeIncident invalidates the incident, list, and monitor keys', async () => {
		server.use(
			http.post(
				`${incidentsPath}/i1/acknowledge`,
				() => new HttpResponse(null, { status: 204 }),
			),
		);
		const queryClient = createTestQueryClient();
		const incident = makeIncident();
		const detail: IncidentDetail = { incident, events: [] };

		queryClient.setQueryData(
			queryKeys.incident(workspaceId, incident.id),
			detail,
		);
		queryClient.setQueryData(
			queryKeys.incidents(workspaceId, { status: 'all' }),
			{
				pages: [{ incidents: [incident] }],
				pageParams: [undefined],
			},
		);
		queryClient.setQueryData(
			queryKeys.monitor(workspaceId, incident.monitor_id),
			{
				id: incident.monitor_id,
			},
		);
		queryClient.setQueryData(queryKeys.monitors(workspaceId), {
			pages: [{ monitors: [] }],
			pageParams: [undefined],
		});

		const { result } = renderHook(
			() => useAcknowledgeIncident(workspaceId, incident.id),
			{ wrapper: wrapperFor(queryClient) },
		);

		await act(async () => {
			await result.current.mutateAsync();
		});

		expect(
			queryClient.getQueryState(queryKeys.incident(workspaceId, incident.id))
				?.isInvalidated,
		).toBe(true);
		expect(
			queryClient.getQueryState(
				queryKeys.incidents(workspaceId, { status: 'all' }),
			)?.isInvalidated,
		).toBe(true);
		expect(
			queryClient.getQueryState(
				queryKeys.monitor(workspaceId, incident.monitor_id),
			)?.isInvalidated,
		).toBe(true);
		expect(
			queryClient.getQueryState(queryKeys.monitors(workspaceId))?.isInvalidated,
		).toBe(true);
	});
});
