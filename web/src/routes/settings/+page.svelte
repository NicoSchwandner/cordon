<script lang="ts">
	import type { SecretRef } from '$lib/shared/api/types';
	import { listSecrets, setSecret, updateSecret, deleteSecret } from '$lib/shared/api/client';

	let activeTab: 'egress' | 'overrides' | 'secrets' = $state('egress');

	const egressAllowlist = [
		'github.com',
		'api.anthropic.com',
		'registry.npmjs.org',
		'nuget.org',
		'*.example.com'
	];

	const tierOverrides = [
		{ pattern: 'DELETE FROM audit_log', tier: 4 },
		{ pattern: 'DROP TABLE temp_*', tier: 2 }
	];

	// Secrets state
	let secrets: SecretRef[] = $state([]);
	let loading = $state(false);
	let revealedSecrets: Set<string> = $state(new Set());

	// Form state
	let showForm = $state(false);
	let editingName: string | null = $state(null);
	let formName = $state('');
	let formPlaceholder = $state('');
	let formValue = $state('');
	let formError = $state('');
	let saving = $state(false);

	$effect(() => {
		if (activeTab === 'secrets') loadSecrets();
	});

	async function loadSecrets() {
		loading = true;
		try {
			secrets = await listSecrets();
		} catch {
			secrets = [];
		}
		loading = false;
	}

	function toggleReveal(name: string) {
		const next = new Set(revealedSecrets);
		if (next.has(name)) next.delete(name);
		else next.add(name);
		revealedSecrets = next;
	}

	function maskPlaceholder(placeholder: string): string {
		if (placeholder.length <= 12) return '••••••••••••';
		return placeholder.slice(0, 8) + '••••' + placeholder.slice(-4);
	}

	function startAdd() {
		editingName = null;
		formName = '';
		formPlaceholder = '';
		formValue = '';
		formError = '';
		showForm = true;
	}

	function startEdit(secret: SecretRef) {
		editingName = secret.name;
		formName = secret.name;
		formPlaceholder = secret.placeholder;
		formValue = '';
		formError = '';
		showForm = true;
	}

	function cancelForm() {
		showForm = false;
		editingName = null;
		formError = '';
	}

	async function handleSubmit() {
		if (!formName.trim() || !formValue.trim()) {
			formError = 'Name and value are required';
			return;
		}

		saving = true;
		formError = '';
		try {
			const req = {
				name: formName.trim(),
				placeholder: formPlaceholder.trim() || undefined,
				value: formValue.trim()
			};
			if (editingName) {
				await updateSecret(req);
			} else {
				await setSecret(req);
			}
			showForm = false;
			editingName = null;
			formValue = '';
			await loadSecrets();
		} catch (e) {
			formError = e instanceof Error ? e.message : 'Failed to save secret';
		}
		saving = false;
	}

	async function handleDelete(name: string) {
		if (!confirm(`Delete secret "${name}"? This cannot be undone.`)) return;
		try {
			await deleteSecret(name);
			await loadSecrets();
		} catch (e) {
			alert(e instanceof Error ? e.message : 'Failed to delete secret');
		}
	}
</script>

<div class="space-y-6">
	<h1 class="text-2xl font-semibold tracking-tight text-foreground">Settings</h1>

	<!-- Tabs -->
	<div class="flex gap-1 rounded-xl border border-border bg-surface p-1">
		{#each [
			{ id: 'egress', label: 'Egress Allowlist' },
			{ id: 'overrides', label: 'Tier Overrides' },
			{ id: 'secrets', label: 'Secrets' }
		] as tab}
			<button
				onclick={() => (activeTab = tab.id as typeof activeTab)}
				class="rounded-lg px-4 py-2 text-sm font-medium transition-colors {activeTab === tab.id
					? 'bg-sidebar-active text-foreground'
					: 'text-foreground-muted hover:text-foreground hover:bg-hover-subtle'}"
			>
				{tab.label}
			</button>
		{/each}
	</div>

	<!-- Egress -->
	{#if activeTab === 'egress'}
		<div class="rounded-xl border border-border bg-surface p-5">
			<p class="mb-4 text-sm text-foreground-tertiary">
				Hosts that workspace processes are allowed to connect to. All other egress is blocked.
			</p>
			<ul class="space-y-1">
				{#each egressAllowlist as host}
					<li class="flex items-center gap-2 rounded-lg bg-surface-inset px-3 py-2.5">
						<span class="h-2 w-2 rounded-full bg-success-text"></span>
						<span class="font-mono text-sm text-foreground">{host}</span>
					</li>
				{/each}
			</ul>
			<p class="mt-4 text-xs text-foreground-faint">
				Managed via .cordon.yaml. API-based management coming soon.
			</p>
		</div>
	{/if}

	<!-- Tier Overrides -->
	{#if activeTab === 'overrides'}
		<div class="rounded-xl border border-border bg-surface p-5">
			<p class="mb-4 text-sm text-foreground-tertiary">
				Pattern-based overrides that escalate or de-escalate the default tier for specific
				operations.
			</p>
			<div class="overflow-x-auto">
				<table class="min-w-full text-sm">
					<thead>
						<tr class="border-b border-border">
							<th
								class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-foreground-muted"
								>Pattern</th
							>
							<th
								class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-foreground-muted"
								>Forced Tier</th
							>
						</tr>
					</thead>
					<tbody>
						{#each tierOverrides as override}
							<tr class="border-b border-border-subtle last:border-0">
								<td class="px-4 py-3 font-mono text-foreground">{override.pattern}</td>
								<td class="px-4 py-3">
									<span
										class="inline-block rounded-full bg-surface-raised px-2.5 py-1 text-xs font-medium text-foreground-secondary"
									>
										Tier {override.tier}
									</span>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
			<p class="mt-4 text-xs text-foreground-faint">
				Managed via .cordon.yaml. API-based management coming soon.
			</p>
		</div>
	{/if}

	<!-- Secrets -->
	{#if activeTab === 'secrets'}
		<div class="rounded-xl border border-border bg-surface p-5">
			<div class="mb-4 flex items-center justify-between">
				<p class="text-sm text-foreground-tertiary">
					Secret names and their placeholder tokens. Real values are never exposed to the workspace.
				</p>
				<button
					onclick={startAdd}
					class="shrink-0 rounded-lg bg-primary px-3 py-1.5 text-sm font-medium text-on-primary transition-colors hover:bg-primary-hover"
				>
					Add Secret
				</button>
			</div>

			<!-- Add/Edit form -->
			{#if showForm}
				<form
					onsubmit={(e) => { e.preventDefault(); handleSubmit(); }}
					class="mb-4 rounded-lg border border-border-input bg-surface-inset p-4"
				>
					<div class="space-y-3">
						<div>
							<label for="secret-name" class="mb-1 block text-xs font-medium text-foreground-muted">Name</label>
							<input
								id="secret-name"
								bind:value={formName}
								disabled={editingName !== null}
								placeholder="e.g. DATABASE_URL"
								class="w-full rounded-lg border border-border-input bg-surface px-3 py-2 text-sm text-foreground placeholder:text-foreground-faint focus:border-transparent focus:outline-none focus:ring-2 focus:ring-ring disabled:opacity-50"
							/>
						</div>
						<div>
							<label for="secret-placeholder" class="mb-1 block text-xs font-medium text-foreground-muted">
								Placeholder <span class="font-normal text-foreground-faint">(auto-generated if empty)</span>
							</label>
							<input
								id="secret-placeholder"
								bind:value={formPlaceholder}
								placeholder="e.g. cordon-placeholder-database-url"
								class="w-full rounded-lg border border-border-input bg-surface px-3 py-2 font-mono text-sm text-foreground placeholder:text-foreground-faint focus:border-transparent focus:outline-none focus:ring-2 focus:ring-ring"
							/>
						</div>
						<div>
							<label for="secret-value" class="mb-1 block text-xs font-medium text-foreground-muted">
								Value {#if editingName}<span class="font-normal text-foreground-faint">(enter new value to update)</span>{/if}
							</label>
							<input
								id="secret-value"
								bind:value={formValue}
								type="password"
								placeholder="The real credential value"
								class="w-full rounded-lg border border-border-input bg-surface px-3 py-2 font-mono text-sm text-foreground placeholder:text-foreground-faint focus:border-transparent focus:outline-none focus:ring-2 focus:ring-ring"
							/>
						</div>
						{#if formError}
							<p class="text-sm text-danger-text">{formError}</p>
						{/if}
						<div class="flex gap-2">
							<button
								type="submit"
								disabled={saving}
								class="rounded-lg bg-primary px-4 py-2 text-sm font-medium text-on-primary transition-colors hover:bg-primary-hover disabled:cursor-not-allowed disabled:opacity-50"
							>
								{saving ? 'Saving...' : editingName ? 'Update Secret' : 'Create Secret'}
							</button>
							<button
								type="button"
								onclick={cancelForm}
								class="rounded-lg border border-border-input px-4 py-2 text-sm text-foreground-secondary transition-colors hover:bg-hover-subtle"
							>
								Cancel
							</button>
						</div>
					</div>
				</form>
			{/if}

			<!-- Secret list -->
			{#if loading}
				<p class="py-4 text-center text-sm text-foreground-muted">Loading...</p>
			{:else if secrets.length === 0}
				<div class="rounded-lg border border-border-subtle bg-surface-inset p-6 text-center text-sm text-foreground-muted">
					No secrets configured. Add one to get started.
				</div>
			{:else}
				<div class="space-y-2">
					{#each secrets as secret (secret.name)}
						<div class="flex items-center justify-between rounded-lg bg-surface-inset px-4 py-3">
							<div class="min-w-0 flex-1">
								<span class="font-mono text-sm font-medium text-foreground">{secret.name}</span>
								<button
									onclick={() => toggleReveal(secret.name)}
									class="ml-2 text-xs text-foreground-faint transition-colors hover:text-foreground-muted"
									title={revealedSecrets.has(secret.name) ? 'Hide placeholder' : 'Reveal placeholder'}
								>
									{#if revealedSecrets.has(secret.name)}
										<span class="font-mono">{secret.placeholder}</span>
									{:else}
										<span class="font-mono">{maskPlaceholder(secret.placeholder)}</span>
									{/if}
								</button>
							</div>
							<div class="flex shrink-0 items-center gap-2">
								<span class="rounded-full bg-success-badge-bg px-2.5 py-1 text-xs font-medium text-success-text">
									configured
								</span>
								<button
									onclick={() => startEdit(secret)}
									class="rounded-md px-2 py-1 text-xs text-foreground-muted transition-colors hover:bg-hover-subtle hover:text-foreground"
								>
									Edit
								</button>
								<button
									onclick={() => handleDelete(secret.name)}
									class="rounded-md px-2 py-1 text-xs text-danger-text transition-colors hover:bg-danger-bg"
								>
									Delete
								</button>
							</div>
						</div>
					{/each}
				</div>
			{/if}

			<p class="mt-4 text-xs text-foreground-faint">
				Real credentials are injected at the proxy level and never leave the server.
			</p>
		</div>
	{/if}
</div>
