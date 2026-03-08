import httpx

from iris.utils.logger import log_llm_payload, log_llm_response


class LLMClient:
    def __init__(self, model: str = "unsloth_Qwen3.5-9B-GGUF_Qwen3.5-9B-Q5_K_M"):
        self.model = model
        self.url = "http://localhost:8080/v1/chat/completions"

    def generate(
        self,
        prompt: str,
        temperature: float = 0.2,
    ) -> str:
        """
        Generate a completion from llama.cpp server (OpenAI-compatible API)
        Returns raw text. Caller is responsible for JSON extraction if needed.
        """

        payload = {
            "model": self.model,
            "messages": [
                {"role": "user", "content": prompt},
            ],
            "max_tokens": 32768,
            "temperature": temperature,
            # "top_p": 0.8,
            # "presence_penalty": 1.5,
            "response_format": {"type": "json_object"},
            # "extra_body": {
            #     "top_k": 20,
            #     "chat_template_kwargs": {"enable_thinking": False},
            # },
        }

        # Retry once in case of network hiccup
        try:
            log_llm_payload(payload)
            response = httpx.post(self.url, json=payload, timeout=120)
            response.raise_for_status()
        except Exception as e:
            print("LLM request failed, retrying once...", e)
            response = httpx.post(self.url, json=payload, timeout=120)
            response.raise_for_status()

        log_llm_response(response.text)
        return str(response.json()["choices"][0]["message"]["content"])
