import { apiRequest } from '@/lib/api/client';

export interface BillingPlan {
	key: string;
	name: string;
	monitors: number;
	channels: number;
	api_keys: number;
	price_cents: number;
}

export interface Billing {
	plan: BillingPlan;
	status: string;
	provider: string;
	current_period_end: string | null;
	trial_end: string | null;
	cancel_at_period_end: boolean;
	mode: string;
}

export interface PlanList {
	plans: BillingPlan[];
}

export function getBilling(workspaceId: string): Promise<Billing> {
	return apiRequest<Billing>(`/workspaces/${workspaceId}/billing`);
}

export function listPlans(): Promise<PlanList> {
	return apiRequest<PlanList>('/plans');
}

export function startCheckout(
	workspaceId: string,
	plan: string,
): Promise<{ url: string }> {
	return apiRequest<{ url: string }>(
		`/workspaces/${workspaceId}/billing/checkout`,
		{
			method: 'POST',
			body: { plan },
		},
	);
}

export function cancelSubscription(workspaceId: string): Promise<void> {
	return apiRequest<void>(`/workspaces/${workspaceId}/billing/cancel`, {
		method: 'POST',
	});
}

export function completeSandbox(
	workspaceId: string,
	token: string,
	outcome: 'success' | 'failure' | 'cancel',
): Promise<void> {
	return apiRequest<void>(
		`/workspaces/${workspaceId}/billing/sandbox/complete`,
		{ method: 'POST', body: { token, outcome } },
	);
}
