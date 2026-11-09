import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import type { CreateAPIKeyInput } from '@/lib/api/types';
import { queryKeys } from '@/lib/query/keys';
import { createAPIKey, listAPIKeys, revokeAPIKey } from './api';

export function useAPIKeys(workspaceId: string) {
	return useQuery({
		queryKey: queryKeys.apiKeys(workspaceId),
		queryFn: () => listAPIKeys(workspaceId),
		staleTime: 30_000,
	});
}

export function useCreateAPIKey(workspaceId: string) {
	const queryClient = useQueryClient();

	return useMutation({
		mutationFn: (input: CreateAPIKeyInput) => createAPIKey(workspaceId, input),
		onSuccess: () =>
			queryClient.invalidateQueries({
				queryKey: queryKeys.apiKeys(workspaceId),
			}),
	});
}

export function useRevokeAPIKey(workspaceId: string) {
	const queryClient = useQueryClient();

	return useMutation({
		mutationFn: (keyId: string) => revokeAPIKey(workspaceId, keyId),
		onSuccess: () =>
			queryClient.invalidateQueries({
				queryKey: queryKeys.apiKeys(workspaceId),
			}),
	});
}
