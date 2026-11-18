import { useQuery } from '@tanstack/react-query';
import { queryKeys } from '@/lib/query/keys';
import { getBilling, listPlans } from './api';

export function useBilling(workspaceId: string) {
	return useQuery({
		queryKey: queryKeys.billing(workspaceId),
		queryFn: () => getBilling(workspaceId),
		staleTime: 15_000,
	});
}

export function usePlans() {
	return useQuery({
		queryKey: queryKeys.plans(),
		queryFn: listPlans,
		staleTime: 300_000,
	});
}
