import {
	type UseMutationResult,
	type UseQueryResult,
	useMutation,
	useQuery,
	useQueryClient,
} from '@tanstack/react-query';
import type { IncidentInvestigation } from '@/lib/api/types';
import { queryKeys } from '@/lib/query/keys';
import {
	getIncidentInvestigationLatest,
	startIncidentInvestigation,
} from './api';

const ACTIVE_STATUSES = new Set(['queued', 'running']);

export function useIncidentInvestigation(
	workspaceId: string,
	incidentId: string,
): UseQueryResult<IncidentInvestigation> {
	return useQuery({
		queryKey: queryKeys.incidentInvestigationLatest(workspaceId, incidentId),
		queryFn: ({ signal }) =>
			getIncidentInvestigationLatest(workspaceId, incidentId, signal),
		// A 404 means no investigation yet, which is a normal empty state.
		retry: false,
		staleTime: 0,
		refetchInterval: (query) =>
			ACTIVE_STATUSES.has(query.state.data?.status ?? '') ? 2_000 : false,
	});
}

export function useStartIncidentInvestigation(
	workspaceId: string,
	incidentId: string,
): UseMutationResult<IncidentInvestigation, unknown, void> {
	const queryClient = useQueryClient();
	const latestKey = queryKeys.incidentInvestigationLatest(
		workspaceId,
		incidentId,
	);

	return useMutation({
		mutationFn: () => startIncidentInvestigation(workspaceId, incidentId),
		onSuccess: (investigation) => {
			// 202 and 409 both carry the row to poll, so seed it either way.
			queryClient.setQueryData(latestKey, investigation);
		},
	});
}
