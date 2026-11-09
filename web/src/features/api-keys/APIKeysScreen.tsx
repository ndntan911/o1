import { useState } from 'react';
import { toast } from 'sonner';
import { ConfirmDialog } from '@/components/feedback/ConfirmDialog';
import { EmptyState } from '@/components/feedback/EmptyState';
import { ErrorPanel } from '@/components/feedback/ErrorPanel';
import { PageSkeleton } from '@/components/feedback/Skeletons';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from '@/components/ui/table';
import { isApiProblem } from '@/lib/api/client';
import { problemFromError } from '@/lib/api/problem-from-error';
import type { ApiProblem } from '@/lib/api/problems';
import type { APIKey, Workspace } from '@/lib/api/types';
import { formatRelativeTime } from '@/lib/format';
import { canManageAPIKeys } from '@/lib/roles';
import { CreateAPIKeyDialog } from './CreateAPIKeyDialog';
import { useAPIKeys, useRevokeAPIKey } from './hooks';

export interface APIKeysScreenProps {
	workspace: Workspace;
}

function errorMessage(error: unknown): string {
	if (isApiProblem(error)) {
		return error.problem.detail ?? error.problem.title;
	}
	if (error instanceof Error) return error.message;
	return 'Please try again.';
}

export function APIKeysScreen({ workspace }: APIKeysScreenProps) {
	if (!canManageAPIKeys(workspace.role)) {
		return (
			<section className="space-y-2">
				<h1 className="font-heading text-xl font-semibold">API keys</h1>
				<p className="text-sm text-muted-foreground">
					You do not have access to API key settings.
				</p>
			</section>
		);
	}

	return <APIKeysContent workspace={workspace} />;
}

function APIKeysContent({ workspace }: APIKeysScreenProps) {
	const keysQuery = useAPIKeys(workspace.id);
	const revoke = useRevokeAPIKey(workspace.id);
	const [createOpen, setCreateOpen] = useState(false);
	const [revokeTarget, setRevokeTarget] = useState<APIKey | null>(null);
	const [problem, setProblem] = useState<ApiProblem | null>(null);

	if (keysQuery.isPending) return <PageSkeleton />;

	if (!keysQuery.data) {
		return (
			<ErrorPanel
				title="Could not load API keys"
				message={errorMessage(keysQuery.error)}
				problem={problemFromError(keysQuery.error)}
				onRetry={() => void keysQuery.refetch()}
			/>
		);
	}

	const keys = keysQuery.data.keys;

	return (
		<section className="space-y-6">
			<div className="flex flex-wrap items-center justify-between gap-3">
				<div>
					<h1 className="font-heading text-xl font-semibold">API keys</h1>
					<p className="text-sm text-muted-foreground">
						Keys authenticate scripts with the Bearer scheme and are scoped to{' '}
						{workspace.name}.
					</p>
				</div>
				<Button type="button" onClick={() => setCreateOpen(true)}>
					Create key
				</Button>
			</div>

			{keys.length === 0 ? (
				<EmptyState
					title="No API keys yet."
					description="Create a key to let a script read or automate this workspace."
				/>
			) : (
				<Table>
					<TableHeader>
						<TableRow>
							<TableHead>Name</TableHead>
							<TableHead>Prefix</TableHead>
							<TableHead>Role</TableHead>
							<TableHead>Last used</TableHead>
							<TableHead>Created</TableHead>
							<TableHead>Actions</TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{keys.map((key) => (
							<TableRow key={key.id}>
								<TableCell className="font-medium">{key.name}</TableCell>
								<TableCell>
									<code className="font-mono text-xs">{key.prefix}</code>
								</TableCell>
								<TableCell>
									<Badge variant="outline">{key.role}</Badge>
								</TableCell>
								<TableCell>
									{key.last_used_at
										? formatRelativeTime(key.last_used_at)
										: 'Never used'}
								</TableCell>
								<TableCell>
									{new Date(key.created_at).toLocaleDateString()}
								</TableCell>
								<TableCell>
									<Button
										type="button"
										variant="outline"
										size="sm"
										aria-label={`Revoke ${key.name}`}
										onClick={() => {
											setProblem(null);
											setRevokeTarget(key);
										}}
									>
										Revoke
									</Button>
								</TableCell>
							</TableRow>
						))}
					</TableBody>
				</Table>
			)}

			{createOpen ? (
				<CreateAPIKeyDialog
					workspaceId={workspace.id}
					onClose={() => setCreateOpen(false)}
				/>
			) : null}

			<ConfirmDialog
				open={revokeTarget !== null}
				title="Revoke API key?"
				description={
					revokeTarget
						? `${revokeTarget.name} will stop working immediately. Requests using it will be rejected, but audit history is retained.`
						: undefined
				}
				confirmLabel="Revoke key"
				pending={revoke.isPending}
				problem={problem}
				onConfirm={() => {
					if (!revokeTarget) return;
					revoke.mutate(revokeTarget.id, {
						onSuccess: () => {
							toast.success('API key revoked');
							setRevokeTarget(null);
							setProblem(null);
						},
						onError: (error) => setProblem(problemFromError(error)),
					});
				}}
				onOpenChange={(open) => {
					if (!open) {
						setRevokeTarget(null);
						setProblem(null);
					}
				}}
			/>
		</section>
	);
}
