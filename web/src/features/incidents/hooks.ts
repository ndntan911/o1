import {
	useInfiniteQuery,
	useMutation,
	useQuery,
	useQueryClient,
} from '@tanstack/react-query';
import type { IncidentFilters, Role } from '@/lib/api/types';
import { queryKeys } from '@/lib/query/keys';
import { nextPageParam } from '@/lib/query/pagination';
import { roleAtLeast } from '@/lib/roles';
import {
	acknowledgeIncident,
	getIncident,
	listIncidentDeliveries,
	listIncidents,
} from './api';

export function useIncidents(
	workspaceId: string,
	filters: IncidentFilters,
	limit = 50,
) {
	return useInfiniteQuery({
		queryKey: queryKeys.incidents(workspaceId, filters),
		queryFn: ({ pageParam, signal }) =>
			listIncidents(workspaceId, filters, pageParam, signal, limit),
		initialPageParam: undefined as string | undefined,
		getNextPageParam: nextPageParam,
		staleTime: 15_000,
		refetchInterval: 15_000,
	});
}

export function useIncident(workspaceId: string, incidentId: string) {
	return useQuery({
		queryKey: queryKeys.incident(workspaceId, incidentId),
		queryFn: ({ signal }) => getIncident(workspaceId, incidentId, signal),
		staleTime: 15_000,
		refetchInterval: 15_000,
	});
}

export function useIncidentDeliveries(
	workspaceId: string,
	incidentId: string,
	role: Role,
) {
	return useQuery({
		queryKey: queryKeys.incidentDeliveries(workspaceId, incidentId),
		queryFn: ({ signal }) =>
			listIncidentDeliveries(workspaceId, incidentId, signal),
		enabled: roleAtLeast(role, 'admin'),
		staleTime: 30_000,
		refetchInterval: 30_000,
	});
}

export function useAcknowledgeIncident(
	workspaceId: string,
	incidentId: string,
) {
	const queryClient = useQueryClient();

	return useMutation({
		mutationFn: () => acknowledgeIncident(workspaceId, incidentId),
		onSuccess: () =>
			Promise.all([
				queryClient.invalidateQueries({
					queryKey: queryKeys.incident(workspaceId, incidentId),
				}),
				queryClient.invalidateQueries({
					queryKey: ['incidents', workspaceId],
				}),
				queryClient.invalidateQueries({
					queryKey: ['monitor', workspaceId],
				}),
				queryClient.invalidateQueries({
					queryKey: queryKeys.monitors(workspaceId),
				}),
			]),
	});
}
