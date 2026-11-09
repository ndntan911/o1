import { describe, expect, test, vi } from 'vitest';
import { act, renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { HttpResponse, http } from 'msw';
import type { ReactNode } from 'react';
import { server } from '@/test/msw/server';
import { createTestQueryClient } from '@/test/render';
import { queryKeys } from '@/lib/query/keys';
import type { APIKey } from '@/lib/api/types';
import { canManageAPIKeys } from '@/lib/roles';
import { useAPIKeys, useCreateAPIKey, useRevokeAPIKey } from './hooks';

const ORIGIN = 'http://localhost:5173';
const keysPath = `${ORIGIN}/api/v1/workspaces/w1/api-keys`;
const requests = vi.hoisted(() => ({ list: 0 }));

const key: APIKey = {
	id: 'k1',
	name: 'CI deploy',
	prefix: 'o1_abcdefghijkl',
	role: 'member',
	created_by: 'u1',
	created_at: '2026-11-09T09:00:00Z',
	last_used_at: null,
};

function wrapper(queryClient: QueryClient) {
	return function Wrapper({ children }: { children: ReactNode }) {
		return (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
	};
}

describe('API key hooks', () => {
	test('useAPIKeys uses the apiKeys key', async () => {
		server.use(
			http.get(keysPath, () => {
				requests.list += 1;
				return HttpResponse.json({ keys: [key] });
			}),
		);
		const client = createTestQueryClient();
		const { result } = renderHook(() => useAPIKeys('w1'), {
			wrapper: wrapper(client),
		});

		await waitFor(() => expect(result.current.isSuccess).toBe(true));

		expect(
			client.getQueryCache().findAll({ queryKey: queryKeys.apiKeys('w1') }),
		).toHaveLength(1);
		expect(result.current.data).toEqual({ keys: [key] });
		expect(requests.list).toBeGreaterThan(0);
	});

	test('creating a key invalidates the apiKeys list and returns the secret', async () => {
		server.use(
			http.post(keysPath, () =>
				HttpResponse.json(
					{ key, secret: 'o1_abcdefghijkl_secretonetime' },
					{ status: 201 },
				),
			),
		);
		const client = createTestQueryClient();
		client.setQueryData(queryKeys.apiKeys('w1'), { keys: [] });
		const { result } = renderHook(() => useCreateAPIKey('w1'), {
			wrapper: wrapper(client),
		});

		const created = await act(async () =>
			result.current.mutateAsync({ name: 'CI deploy', role: 'member' }),
		);

		expect(created).toEqual({
			key,
			secret: 'o1_abcdefghijkl_secretonetime',
		});
		expect(client.getQueryState(queryKeys.apiKeys('w1'))?.isInvalidated).toBe(
			true,
		);
	});

	test('revoking a key invalidates the apiKeys list', async () => {
		server.use(
			http.delete(
				`${keysPath}/k1`,
				() => new HttpResponse(null, { status: 204 }),
			),
		);
		const client = createTestQueryClient();
		client.setQueryData(queryKeys.apiKeys('w1'), { keys: [key] });
		const { result } = renderHook(() => useRevokeAPIKey('w1'), {
			wrapper: wrapper(client),
		});

		await act(async () => {
			await result.current.mutateAsync('k1');
		});

		expect(client.getQueryState(queryKeys.apiKeys('w1'))?.isInvalidated).toBe(
			true,
		);
	});

	test('only admins and owners can manage API keys', () => {
		expect(canManageAPIKeys('admin')).toBe(true);
		expect(canManageAPIKeys('owner')).toBe(true);
		expect(canManageAPIKeys('member')).toBe(false);
		expect(canManageAPIKeys('viewer')).toBe(false);
	});
});
