{% if last_tools %}
## Last 5 Tool Usage

{% for tool in last_tools %}
{{ loop.index }}. {{ tool }}
{% endfor %}
{% endif %}

{% if last_tool_result %}
## Last Tool Result

{{ last_tool_result }}
{% endif %}
