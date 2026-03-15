import httpx

from iris.utils.logger import log_llm_payload, log_llm_response


class LLMClient:
    def __init__(
        self, model: str = "bartowski_Qwen_Qwen3.5-9B-GGUF_Qwen_Qwen3.5-9B-Q5_K_M"
    ):
        self.model = model
        self.url = "http://localhost:8080/v1/chat/completions"

    def generate(
        self,
        system_prompt: str,
        user_prompt: str,
        temperature: float = 0.2,
        top_p: float = 0.95,
        top_k: int = 40,
        min_p: float = 0.05,
        presence_penalty: float = 0.0,
        repetition_penalty: float = 1.1,
    ) -> str:
        payload = {
            "model": self.model,
            "messages": [
                {"role": "system", "content": system_prompt},
                {"role": "user", "content": user_prompt},
            ],
            "max_tokens": 32768,
            "temperature": temperature,
            "top_p": top_p,
            "presence_penalty": presence_penalty,
            "response_format": {"type": "json_object"},
            "extra_body": {
                "top_k": top_k,
                "min_p": min_p,
                "repetition_penalty": repetition_penalty,
                "chat_template_kwargs": {"enable_thinking": False},
            },
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
