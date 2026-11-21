import {
	type UseMutationResult,
	type UseQueryResult,
	useMutation,
	useQuery,
	useQueryClient,
} from '@tanstack/react-query';
import type { IncidentInsight } from '@/lib/api/types';
import { queryKeys } from '@/lib/query/keys';
import { generateIncidentInsight, getIncidentInsight } from './api';

export function useIncidentInsight(
	workspaceId: string,
	incidentId: string,
): UseQueryResult<IncidentInsight> {
	return useQuery({
		queryKey: queryKeys.incidentInsight(workspaceId, incidentId),
		queryFn: ({ signal }) => getIncidentInsight(workspaceId, incidentId, signal),
		// A 404 means no analysis yet, which is a normal empty state.
		retry: false,
		staleTime: 60_000,
	});
}

export function useGenerateIncidentInsight(
	workspaceId: string,
	incidentId: string,
): UseMutationResult<IncidentInsight, unknown, boolean> {
	const queryClient = useQueryClient();

	return useMutation({
		mutationFn: (force: boolean) =>
			generateIncidentInsight(workspaceId, incidentId, force),
		onSuccess: (insight) => {
			queryClient.setQueryData(
				queryKeys.incidentInsight(workspaceId, incidentId),
				insight,
			);
		},
	});
}
