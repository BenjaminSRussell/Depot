// A thin wrapper around Stripe.js, loaded from js.stripe.com only when the customer reaches the payment step (PCI SAQ A: the card
// and bank details are typed into Stripe's own iframe and go straight to Stripe; this page never sees them). No npm dependency:
// the script tag is the integration Stripe documents, and only the few calls the portal makes are typed here.

export interface StripeError {
  type?: string;
  code?: string;
  decline_code?: string;
  message?: string;
}

export interface StripeElement {
  mount(target: HTMLElement): void;
  destroy(): void;
}

export interface StripeElements {
  create(type: "payment", options?: Record<string, unknown>): StripeElement;
}

export interface StripeInstance {
  elements(options: { clientSecret: string; appearance?: Record<string, unknown> }): StripeElements;
  confirmPayment(options: {
    elements: StripeElements;
    confirmParams: { return_url: string };
    redirect: "if_required";
  }): Promise<{ error?: StripeError; paymentIntent?: { id: string; status: string } }>;
}

export type StripeFactory = (publishableKey: string, options?: { stripeAccount?: string }) => StripeInstance;

declare global {
  interface Window {
    Stripe?: StripeFactory;
  }
}

/** The only place Stripe.js may come from. */
export const STRIPE_JS = "https://js.stripe.com/v3/";

let loading: Promise<StripeFactory> | null = null;

/** Adds the Stripe.js script tag once and resolves when Stripe is ready. A failed load can be retried. */
export function loadStripeJs(): Promise<StripeFactory> {
  if (window.Stripe) return Promise.resolve(window.Stripe);
  if (!loading) {
    loading = new Promise<StripeFactory>((resolve, reject) => {
      const script = document.createElement("script");
      script.src = STRIPE_JS;
      script.async = true;
      script.onload = () => (window.Stripe ? resolve(window.Stripe) : reject(new Error("Stripe.js did not start")));
      script.onerror = () => {
        loading = null;
        script.remove();
        reject(new Error("Stripe.js could not be loaded"));
      };
      document.head.append(script);
    });
  }
  return loading;
}

/** What to tell the customer when Stripe refuses a payment in the browser (a declined card, a closed bank account). */
export function stripeErrorText(error: StripeError): string {
  const code = error.decline_code || error.code || "";
  switch (code) {
    case "insufficient_funds":
      return "There was not enough money in the account. Try another card or bank account.";
    case "expired_card":
      return "The card has expired. Use another card.";
    case "incorrect_cvc":
    case "invalid_cvc":
      return "The card's security code is wrong. Check it and try again.";
    case "incorrect_number":
    case "invalid_number":
      return "The card number is wrong. Check it and try again.";
    case "payment_intent_authentication_failure":
    case "authentication_required":
      return "Your bank asked for a confirmation that was not completed. Try again and finish the confirmation.";
    case "card_declined":
    case "generic_decline":
    case "do_not_honor":
      return "Your card was declined. Try another card or contact your bank.";
  }
  // Card and validation errors from Stripe are written for customers; anything else gets a general sentence.
  if ((error.type === "card_error" || error.type === "validation_error") && error.message) return error.message;
  return "The payment did not go through. Try again or use another payment method.";
}
