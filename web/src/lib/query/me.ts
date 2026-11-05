import { queryOptions, useQuery } from '@tanstack/react-query';
import { apiRequest } from '@/lib/api/client';
import type { Me } from '@/lib/api/types';
import { queryKeys } from './keys';

export function getMe(): Promise<Me> {
	return apiRequest<Me>('/me');
}

export const meQueryOptions = () =>
	queryOptions({ queryKey: queryKeys.me, queryFn: getMe, staleTime: 60_000 });

export function useMe() {
	return useQuery(meQueryOptions());
}
