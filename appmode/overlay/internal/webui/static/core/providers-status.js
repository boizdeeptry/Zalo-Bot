// Shared readiness projection for Providers labels and the Combos model picker.
// `connection_mode` is server-owned runtime metadata copied onto each provider body; kind/system
// must never infer a different credential domain.
export function isProviderConnected(provider) {
  if (!provider || provider.credential_unreadable === true) return false;
  switch (provider.connection_mode) {
    case "account":
      return Array.isArray(provider.accounts)
        && provider.accounts.some((account) => account?.enabled === true);
    case "credential":
      return provider.credential_configured === true;
    case "none":
    default:
      return false;
  }
}
