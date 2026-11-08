import { describe, expect, test } from 'vitest';
import { act, renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { HttpResponse, http } from 'msw';
import type { ReactNode } from 'react';
import { server } from '@/test/msw/server';
import { createTestQueryClient } from '@/test/render';
import { queryKeys } from '@/lib/query/keys';
import { usePublicStatus, useStatusPage, useUpdateStatusPage } from './hooks';

const ORIGIN = 'http://localhost:5173';
const configPath = `${ORIGIN}/api/v1/workspaces/w1/status-page`;

function wrapper(queryClient: QueryClient) {
	return function Wrapper({ children }: { children: ReactNode }) {
		return (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
	};
}

describe('status page hooks', () => {
	test('useStatusPage uses the statusPage key', async () => {
		server.use(
			http.get(configPath, () =>
				HttpResponse.json({
					enabled: false,
					title: 'Acme',
					description: '',
					monitor_ids: [],
				}),
			),
		);
		const client = createTestQueryClient();
		const { result } = renderHook(() => useStatusPage('w1'), {
			wrapper: wrapper(client),
		});
		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		expect(
			client.getQueryCache().findAll({ queryKey: queryKeys.statusPage('w1') }),
		).toHaveLength(1);
	});

	test('the update mutation invalidates config and public caches', async () => {
		server.use(
			http.put(configPath, () =>
				HttpResponse.json({
					enabled: true,
					title: 'Acme',
					description: '',
					monitor_ids: ['m1'],
				}),
			),
		);
		const client = createTestQueryClient();
		client.setQueryData(queryKeys.statusPage('w1'), {
			enabled: false,
			title: 'Acme',
			description: '',
			monitor_ids: [],
		});
		client.setQueryData(queryKeys.publicStatus('acme'), {
			workspace: { name: 'Acme', slug: 'acme' },
		});

		const { result } = renderHook(() => useUpdateStatusPage('w1'), {
			wrapper: wrapper(client),
		});
		await act(async () => {
			await result.current.mutateAsync({
				enabled: true,
				title: 'Acme',
				description: '',
				monitor_ids: ['m1'],
			});
		});

		expect(
			client.getQueryState(queryKeys.statusPage('w1'))?.isInvalidated,
		).toBe(true);
		expect(
			client.getQueryState(queryKeys.publicStatus('acme'))?.isInvalidated,
		).toBe(true);
	});

	test('usePublicStatus does not retry a 404', async () => {
		let calls = 0;
		server.use(
			http.get(`${ORIGIN}/api/v1/public/status/missing`, () => {
				calls += 1;
				return HttpResponse.json(
					{
						type: 'about:blank',
						title: 'Not Found',
						status: 404,
						code: 'not_found',
					},
					{
						status: 404,
						headers: { 'Content-Type': 'application/problem+json' },
					},
				);
			}),
		);
		const client = createTestQueryClient();
		const { result } = renderHook(() => usePublicStatus('missing'), {
			wrapper: wrapper(client),
		});
		await waitFor(() => expect(result.current.isError).toBe(true));
		expect(calls).toBe(1);
	});

	test('usePublicStatus overrides a retrying client default', async () => {
		let calls = 0;
		server.use(
			http.get(`${ORIGIN}/api/v1/public/status/missing`, () => {
				calls += 1;
				return HttpResponse.json(
					{
						type: 'about:blank',
						title: 'Not Found',
						status: 404,
						code: 'not_found',
					},
					{
						status: 404,
						headers: { 'Content-Type': 'application/problem+json' },
					},
				);
			}),
		);
		const client = new QueryClient({
			defaultOptions: {
				queries: { retry: 2, retryDelay: 0 },
				mutations: { retry: false },
			},
		});
		const { result } = renderHook(() => usePublicStatus('missing'), {
			wrapper: wrapper(client),
		});
		await waitFor(() => expect(result.current.isError).toBe(true));
		expect(calls).toBe(1);
	});
});
