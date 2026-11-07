import { describe, expect, test } from 'vitest';
import { act, renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { HttpResponse, http } from 'msw';
import type { ReactNode } from 'react';
import { server } from '@/test/msw/server';
import { createTestQueryClient } from '@/test/render';
import { queryKeys } from '@/lib/query/keys';
import type { MonitorChannelListResponse } from '@/lib/api/types';
import { useMonitorAlertChannels, useSetMonitorChannel } from './hooks';

const ORIGIN = 'http://localhost:5173';
const path = `${ORIGIN}/api/v1/workspaces/w1/monitors/m1/alert-channels`;

function wrapper(queryClient: QueryClient) {
	return function Wrapper({ children }: { children: ReactNode }) {
		return (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
	};
}

function seed(client: QueryClient): MonitorChannelListResponse {
	const data: MonitorChannelListResponse = {
		channels: [
			{
				id: 'c1',
				kind: 'email',
				name: 'Email',
				channel_enabled: true,
				enabled: true,
			},
			{
				id: 'c2',
				kind: 'webhook',
				name: 'Deploy hook',
				channel_enabled: true,
				enabled: true,
			},
		],
	};
	client.setQueryData(queryKeys.monitorAlertChannels('w1', 'm1'), data);
	return data;
}

describe('monitor alert channel hooks', () => {
	test('the list query uses the monitorAlertChannels key', async () => {
		server.use(http.get(path, () => HttpResponse.json({ channels: [] })));
		const client = createTestQueryClient();
		const { result } = renderHook(() => useMonitorAlertChannels('w1', 'm1'), {
			wrapper: wrapper(client),
		});

		await waitFor(() => expect(result.current.isSuccess).toBe(true));
		const queries = client.getQueryCache().findAll({
			queryKey: queryKeys.monitorAlertChannels('w1', 'm1'),
		});
		expect(queries).toHaveLength(1);
	});

	test('toggling flips only the target channel and keeps it after success', async () => {
		server.use(
			http.patch(`${path}/c2`, () => new HttpResponse(null, { status: 204 })),
		);
		const client = createTestQueryClient();
		seed(client);
		const { result } = renderHook(() => useSetMonitorChannel('w1', 'm1'), {
			wrapper: wrapper(client),
		});

		await act(async () => {
			await result.current.mutateAsync({ channelId: 'c2', enabled: false });
		});

		const data = client.getQueryData<MonitorChannelListResponse>(
			queryKeys.monitorAlertChannels('w1', 'm1'),
		);
		expect(data?.channels.find((channel) => channel.id === 'c2')?.enabled).toBe(
			false,
		);
		expect(data?.channels.find((channel) => channel.id === 'c1')?.enabled).toBe(
			true,
		);
	});

	test('a failed toggle rolls the cached list back', async () => {
		server.use(
			http.patch(`${path}/c2`, () =>
				HttpResponse.json(
					{ type: 'about:blank', title: 'Error', status: 500 },
					{ status: 500 },
				),
			),
		);
		const client = createTestQueryClient();
		seed(client);
		const { result } = renderHook(() => useSetMonitorChannel('w1', 'm1'), {
			wrapper: wrapper(client),
		});

		await act(async () => {
			await expect(
				result.current.mutateAsync({ channelId: 'c2', enabled: false }),
			).rejects.toBeTruthy();
		});

		const data = client.getQueryData<MonitorChannelListResponse>(
			queryKeys.monitorAlertChannels('w1', 'm1'),
		);
		expect(data?.channels.find((channel) => channel.id === 'c2')?.enabled).toBe(
			true,
		);
	});
});
