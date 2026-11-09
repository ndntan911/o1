import { describe, expect, test } from 'vitest';
import { act, renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { HttpResponse, http } from 'msw';
import type { ReactNode } from 'react';
import { server } from '@/test/msw/server';
import { createTestQueryClient } from '@/test/render';
import { queryKeys } from '@/lib/query/keys';
import {
	useDisableTOTP,
	useEnableTOTP,
	useRegenerateRecoveryCodes,
	useSetupTOTP,
	useTOTPState,
} from './hooks';

const ORIGIN = 'http://localhost:5173';
const base = `${ORIGIN}/api/v1/auth/2fa`;

const setup = {
	secret: 'JBSWY3DPEHPK3PXP',
	otpauth_uri: 'otpauth://totp/alice?secret=JBSWY3DPEHPK3PXP',
};

const recoveryCodes = ['aaaaa-bbbbb', 'ccccc-ddddd'];

function wrapper(queryClient: QueryClient) {
	return function Wrapper({ children }: { children: ReactNode }) {
		return (
			<QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
		);
	};
}

describe('account 2FA hooks', () => {
	test('useTOTPState uses the totpState key', async () => {
		server.use(
			http.get(base, () =>
				HttpResponse.json({ enabled: false, recovery_codes_remaining: 0 }),
			),
		);
		const client = createTestQueryClient();
		const { result } = renderHook(() => useTOTPState(), {
			wrapper: wrapper(client),
		});

		await waitFor(() => expect(result.current.isSuccess).toBe(true));

		expect(
			client.getQueryCache().findAll({ queryKey: queryKeys.totpState }),
		).toHaveLength(1);
		expect(result.current.data).toEqual({
			enabled: false,
			recovery_codes_remaining: 0,
		});
	});

	test('useSetupTOTP returns the one-time secret without caching it', async () => {
		server.use(http.post(`${base}/setup`, () => HttpResponse.json(setup)));
		const client = createTestQueryClient();
		const { result } = renderHook(() => useSetupTOTP(), {
			wrapper: wrapper(client),
		});

		const created = await act(async () =>
			result.current.mutateAsync({ password: 'correct horse battery' }),
		);

		expect(created).toEqual(setup);
		expect(client.getQueryState(queryKeys.totpState)).toBeUndefined();
	});

	test('enabling invalidates the totpState query', async () => {
		server.use(
			http.post(`${base}/enable`, () =>
				HttpResponse.json({ recovery_codes: recoveryCodes }),
			),
		);
		const client = createTestQueryClient();
		client.setQueryData(queryKeys.totpState, {
			enabled: false,
			recovery_codes_remaining: 0,
		});
		const { result } = renderHook(() => useEnableTOTP(), {
			wrapper: wrapper(client),
		});

		await act(async () => {
			await result.current.mutateAsync({
				password: 'correct horse battery',
				code: '123456',
			});
		});

		expect(client.getQueryState(queryKeys.totpState)?.isInvalidated).toBe(true);
	});

	test('disabling invalidates the totpState query', async () => {
		server.use(
			http.post(
				`${base}/disable`,
				() => new HttpResponse(null, { status: 204 }),
			),
		);
		const client = createTestQueryClient();
		client.setQueryData(queryKeys.totpState, {
			enabled: true,
			recovery_codes_remaining: 10,
		});
		const { result } = renderHook(() => useDisableTOTP(), {
			wrapper: wrapper(client),
		});

		await act(async () => {
			await result.current.mutateAsync({
				password: 'correct horse battery',
				code: '123456',
			});
		});

		expect(client.getQueryState(queryKeys.totpState)?.isInvalidated).toBe(true);
	});

	test('regenerating recovery codes invalidates the totpState query', async () => {
		server.use(
			http.post(`${base}/recovery-codes`, () =>
				HttpResponse.json({ recovery_codes: recoveryCodes }),
			),
		);
		const client = createTestQueryClient();
		client.setQueryData(queryKeys.totpState, {
			enabled: true,
			recovery_codes_remaining: 1,
		});
		const { result } = renderHook(() => useRegenerateRecoveryCodes(), {
			wrapper: wrapper(client),
		});

		await act(async () => {
			await result.current.mutateAsync({
				password: 'correct horse battery',
				code: '123456',
			});
		});

		expect(client.getQueryState(queryKeys.totpState)?.isInvalidated).toBe(true);
	});
});
