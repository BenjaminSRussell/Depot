import { copy } from "../../shell/copy";

export type ProposalState = "PENDING" | "APPROVED" | "REJECTED" | "EXPIRED" | "FAILED";

export const canDecide = (state: ProposalState): boolean => state === "PENDING";

export const showsAskAgain = (state: ProposalState): boolean => state === "EXPIRED";

export const stateLabel = (state: ProposalState): string => copy.proposal.stateLabel[state];

export const stateAnnouncement = (state: ProposalState): string => copy.proposal.announce(stateLabel(state));
