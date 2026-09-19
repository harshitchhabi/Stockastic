// Package news is the two-tier dispatcher: fund-manager feed at T=0, public feed after
// the configured lead time, regime announcements to everyone at once. Release timestamps
// per feed are persisted for dispute resolution.
package news
