import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import type { StatusPageConfigInput } from '@/lib/api/types';
import { queryKeys } from '@/lib/query/keys';
import { getPublicStatus, getStatusPage, updateStatusPage } from './api';

export function useStatusPage(workspaceId: string) {
	return useQuery({
		queryKey: queryKeys.statusPage(workspaceId),
		queryFn: () => getStatusPage(workspaceId),
		staleTime: 30_000,
	});
}

export function useUpdateStatusPage(workspaceId: string) {
	const queryClient = useQueryClient();

	return useMutation({
		mutationFn: (input: StatusPageConfigInput) =>
			updateStatusPage(workspaceId, input),
		onSuccess: () =>
			Promise.all([
				queryClient.invalidateQueries({
					queryKey: queryKeys.statusPage(workspaceId),
				}),
				queryClient.invalidateQueries({ queryKey: ['public-status'] }),
			]),
	});
}

export function usePublicStatus(slug: string) {
	return useQuery({
		queryKey: queryKeys.publicStatus(slug),
		queryFn: () => getPublicStatus(slug),
		retry: false,
		staleTime: 15_000,
	});
}
