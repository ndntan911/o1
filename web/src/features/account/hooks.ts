import {
	useMutation,
	useQuery,
	useQueryClient,
	type QueryClient,
} from '@tanstack/react-query';
import { queryKeys } from '@/lib/query/keys';
import {
	disableTOTP,
	enableTOTP,
	getTOTPState,
	regenerateRecoveryCodes,
	setupTOTP,
} from './api';

export type TOTPMutationAction = 'setup' | 'enable' | 'disable' | 'regenerate';

export function totpMutationKey(
	action: TOTPMutationAction,
	scope: string,
): readonly ['totp', TOTPMutationAction, string] {
	return ['totp', action, scope] as const;
}

/**
 * Drop a completed TOTP mutation from the cache. Mutation state retains its
 * variables and result (passwords, one-time secrets and recovery codes), so
 * removing the entry is what guarantees they are not cached.
 */
export function evictTOTPMutation(
	queryClient: QueryClient,
	action: TOTPMutationAction,
	scope: string,
): void {
	const cache = queryClient.getMutationCache();
	const mutation = cache.find({ mutationKey: totpMutationKey(action, scope) });
	if (mutation) cache.remove(mutation);
}

export function useTOTPState() {
	return useQuery({
		queryKey: queryKeys.totpState,
		queryFn: getTOTPState,
		staleTime: 30_000,
	});
}

export function useSetupTOTP(scope = 'default') {
	return useMutation({
		mutationKey: totpMutationKey('setup', scope),
		mutationFn: setupTOTP,
	});
}

export function useEnableTOTP(scope = 'default') {
	const queryClient = useQueryClient();

	return useMutation({
		mutationKey: totpMutationKey('enable', scope),
		mutationFn: enableTOTP,
		onSuccess: () =>
			queryClient.invalidateQueries({ queryKey: queryKeys.totpState }),
	});
}

export function useDisableTOTP(scope = 'default') {
	const queryClient = useQueryClient();

	return useMutation({
		mutationKey: totpMutationKey('disable', scope),
		mutationFn: disableTOTP,
		onSuccess: () =>
			queryClient.invalidateQueries({ queryKey: queryKeys.totpState }),
	});
}

export function useRegenerateRecoveryCodes(scope = 'default') {
	const queryClient = useQueryClient();

	return useMutation({
		mutationKey: totpMutationKey('regenerate', scope),
		mutationFn: regenerateRecoveryCodes,
		onSuccess: () =>
			queryClient.invalidateQueries({ queryKey: queryKeys.totpState }),
	});
}
