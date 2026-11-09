import {
	useCallback,
	useEffect,
	useId,
	useMemo,
	useState,
	type FormEvent,
} from 'react';
import { toast } from 'sonner';
import { useQueryClient, type QueryClient } from '@tanstack/react-query';
import { ProblemAlert } from '@/components/feedback/ProblemAlert';
import { Button } from '@/components/ui/button';
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from '@/components/ui/select';
import { problemFromError } from '@/lib/api/problem-from-error';
import type { ApiProblem } from '@/lib/api/problems';
import type { APIKeyRole, CreateAPIKeyInput } from '@/lib/api/types';
import { createAPIKeyMutationKey, useCreateAPIKey } from './hooks';

export interface CreateAPIKeyDialogProps {
	workspaceId: string;
	onClose: () => void;
}

const ROLES: Array<{ value: APIKeyRole; label: string }> = [
	{ value: 'viewer', label: 'Viewer' },
	{ value: 'member', label: 'Member' },
];

function evictMutation(
	queryClient: QueryClient,
	mutationKey: readonly unknown[],
): void {
	const cache = queryClient.getMutationCache();
	const mutation = cache.find({ mutationKey });
	if (mutation) cache.remove(mutation);
}

export function CreateAPIKeyDialog({
	workspaceId,
	onClose,
}: CreateAPIKeyDialogProps) {
	const queryClient = useQueryClient();
	const mutationScope = useId();
	const mutationKey = useMemo(
		() => createAPIKeyMutationKey(workspaceId, mutationScope),
		[workspaceId, mutationScope],
	);
	const create = useCreateAPIKey(workspaceId, mutationScope);
	const resetCreate = create.reset;
	const [name, setName] = useState('');
	const [role, setRole] = useState<APIKeyRole>('viewer');
	const [secret, setSecret] = useState<string | null>(null);
	const [problem, setProblem] = useState<ApiProblem | null>(null);

	const discardCreateMutation = useCallback(() => {
		// reset() only detaches the observer and schedules GC; removing the entry
		// from MutationCache is what clears the one-time secret immediately.
		resetCreate();
		evictMutation(queryClient, mutationKey);
	}, [queryClient, mutationKey, resetCreate]);

	useEffect(
		() => () => {
			discardCreateMutation();
		},
		[discardCreateMutation],
	);

	function handleClose() {
		// The secret exists only in this component's state. Clearing it and
		// evicting the mutation guarantees a reopened dialog can never show it.
		setSecret(null);
		setName('');
		setRole('viewer');
		setProblem(null);
		discardCreateMutation();
		onClose();
	}

	function handleSubmit(event: FormEvent<HTMLFormElement>) {
		event.preventDefault();
		setProblem(null);

		const input: CreateAPIKeyInput = { name: name.trim(), role };
		create.mutate(input, {
			onSuccess: (created) => {
				setSecret(created.secret);
				discardCreateMutation();
			},
			onError: (error) => setProblem(problemFromError(error)),
		});
	}

	async function handleCopy() {
		if (!secret) return;
		try {
			await navigator.clipboard.writeText(secret);
			toast.success('API key copied');
		} catch {
			toast.error('Could not copy the key');
		}
	}

	return (
		<Dialog open onOpenChange={(open) => !open && handleClose()}>
			<DialogContent className="sm:max-w-md">
				<DialogHeader>
					<DialogTitle>Create API key</DialogTitle>
					<DialogDescription>
						Create a key to authenticate scripts against this workspace.
					</DialogDescription>
				</DialogHeader>

				{secret ? (
					<div className="space-y-4">
						<div className="space-y-2">
							<Label htmlFor="api-key-secret">API key</Label>
							<Input
								id="api-key-secret"
								readOnly
								value={secret}
								className="font-mono"
								onFocus={(event) => event.currentTarget.select()}
							/>
							<p className="text-sm text-destructive">
								Copy this key now. It will never be shown again.
							</p>
						</div>
						<DialogFooter>
							<Button
								type="button"
								variant="outline"
								onClick={() => void handleCopy()}
							>
								Copy key
							</Button>
							<Button type="button" onClick={handleClose}>
								Done
							</Button>
						</DialogFooter>
					</div>
				) : (
					<form className="space-y-4" onSubmit={handleSubmit}>
						{problem ? <ProblemAlert problem={problem} /> : null}

						<div className="space-y-2">
							<Label htmlFor="api-key-name">Name</Label>
							<Input
								id="api-key-name"
								value={name}
								maxLength={60}
								onChange={(event) => {
									setName(event.target.value);
									setProblem(null);
								}}
								required
							/>
						</div>

						<div className="space-y-2">
							<Label htmlFor="api-key-role">Role</Label>
							<Select
								value={role}
								onValueChange={(value) => setRole(value as APIKeyRole)}
							>
								<SelectTrigger
									id="api-key-role"
									aria-label="Role"
									className="w-full"
								>
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									{ROLES.map((option) => (
										<SelectItem key={option.value} value={option.value}>
											{option.label}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>

						<DialogFooter>
							<Button
								type="button"
								variant="outline"
								onClick={handleClose}
								disabled={create.isPending}
							>
								Cancel
							</Button>
							<Button type="submit" disabled={create.isPending}>
								{create.isPending ? 'Creating…' : 'Create key'}
							</Button>
						</DialogFooter>
					</form>
				)}
			</DialogContent>
		</Dialog>
	);
}
