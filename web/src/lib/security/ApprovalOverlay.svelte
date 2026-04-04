<script lang="ts">
	import type { ApprovalRequest } from '$lib/shared/api/types';
	import { decideApproval } from '$lib/shared/api/client';
	import { connectApprovalsWS } from '$lib/shared/api/websocket';
	import TierBadge from '$lib/shared/components/TierBadge.svelte';
	import MessageBanner from '$lib/shared/components/MessageBanner.svelte';

	let pending: ApprovalRequest[] = $state([]);
	let deciding = $state(false);
	let error = $state('');

	const current = $derived(pending[0] ?? null);

	$effect(() => {
		const cleanup = connectApprovalsWS((req) => {
			pending = [...pending, req];
		});
		return cleanup;
	});

	async function decide(scope: 'one_time' | 'session') {
		if (!current) return;
		deciding = true;
		error = '';
		try {
			await decideApproval(current.id, { scope });
			pending = pending.slice(1);
		} catch (e) {
			error = e instanceof Error ? e.message : 'Approval failed. The backend may be unavailable.';
		}
		deciding = false;
	}

	function deny() {
		pending = pending.slice(1);
	}
</script>

{#if current}
	<div class="fixed inset-0 z-50 flex items-center justify-center backdrop:bg-black/50 bg-black/50">
		<div
			class="w-full max-w-sm rounded-2xl border border-border bg-surface p-6 shadow-lg"
		>
			<div class="mb-4 flex items-center justify-between">
				<h2 class="text-lg font-semibold text-foreground">Approval Required</h2>
				<TierBadge tier={current.tier} />
			</div>

			<div class="mb-4 space-y-2 text-sm">
				<div class="flex justify-between">
					<span class="text-foreground-muted">Operation</span>
					<span class="text-foreground">{current.operation}</span>
				</div>
				<div class="flex justify-between">
					<span class="text-foreground-muted">Target</span>
					<span class="font-mono text-xs text-foreground">{current.target || 'N/A'}</span>
				</div>
				<div class="flex justify-between">
					<span class="text-foreground-muted">Caller</span>
					<span class="text-foreground">{current.caller}</span>
				</div>
				<div class="flex justify-between">
					<span class="text-foreground-muted">Expires</span>
					<span class="text-foreground">{new Date(current.expires_at).toLocaleTimeString()}</span>
				</div>
			</div>

			{#if pending.length > 1}
				<p class="mb-4 text-xs text-foreground-faint">+{pending.length - 1} more pending</p>
			{/if}

			<MessageBanner bind:message={error} />

			<div class="flex gap-2">
				<button
					onclick={() => decide('one_time')}
					disabled={deciding}
					class="flex-1 rounded-lg bg-primary px-4 py-2 text-sm font-medium text-on-primary transition-colors hover:bg-primary-hover disabled:opacity-50 disabled:cursor-not-allowed"
				>
					Approve Once
				</button>
				<button
					onclick={() => decide('session')}
					disabled={deciding}
					class="flex-1 rounded-lg border border-border-input px-4 py-2 text-sm font-medium text-foreground-secondary transition-colors hover:bg-hover-subtle disabled:opacity-50 disabled:cursor-not-allowed"
				>
					Allow Session
				</button>
				<button
					onclick={deny}
					class="flex-1 rounded-lg bg-danger-solid px-4 py-2 text-sm font-medium text-on-danger transition-colors hover:bg-danger-solid-hover"
				>
					Deny
				</button>
			</div>
		</div>
	</div>
{/if}
