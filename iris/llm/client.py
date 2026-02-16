import httpx


class LLMClient:
    def __init__(self, model: str = "qwen3"):
        self.model = model
        self.url = "http://localhost:8000/v1/chat/completions"

    def generate(
        self,
        prompt: str,
        temperature: float = 0.2,
        max_tokens: int = 2048,
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
            "temperature": temperature,
            "max_tokens": max_tokens,
            "response_format": {"type": "json_object"},
        }

        # Retry once in case of network hiccup
        try:
            response = httpx.post(self.url, json=payload, timeout=120)
            response.raise_for_status()
        except Exception as e:
            print("LLM request failed, retrying once...", e)
            response = httpx.post(self.url, json=payload, timeout=120)
            response.raise_for_status()

        return response.json()["choices"][0]["message"]["content"]
